package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// settingsAPI serves the settings page. The local directory is read-only
// information from .env; object storages are edited here and stored in SQLite.
type settingsAPI struct {
	store   *store
	scanner *scanner
	sources *sourceSet
	mu      sync.Mutex
}

type storageInput struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Endpoint   string `json:"endpoint"`
	Region     string `json:"region"`
	Bucket     string `json:"bucket"`
	Prefix     string `json:"prefix"`
	AccessKey  string `json:"accessKey"`
	SecretKey  string `json:"secretKey"`
	Addressing string `json:"addressing"`
	ListV1     bool   `json:"listV1"`
}

func (in storageInput) config() s3Config {
	return s3Config{ID: in.ID, Name: in.Name, Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix,
		AccessKey: in.AccessKey, SecretKey: in.SecretKey, Addressing: in.Addressing, ListV1: in.ListV1}
}

type storageView struct {
	s3Config
	HasSecret bool          `json:"hasSecret"`
	Photos    int           `json:"photos"`
	Broken    int           `json:"broken"`
	Status    *sourceStatus `json:"status"`
}

func (a *settingsAPI) get(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.listStorages()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "读取设置失败"})
		return
	}
	counts, err := a.store.countByOwner()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "读取照片数量失败"})
		return
	}
	statuses := a.scanner.sourceStatuses()
	status := func(id string) *sourceStatus {
		if st, ok := statuses[id]; ok {
			return &st
		}
		return nil
	}
	storages := make([]storageView, 0, len(list))
	for _, c := range list {
		id := storageSourceID(c.ID)
		storages = append(storages, storageView{s3Config: c, HasSecret: c.SecretKey != "", Photos: counts[id].OK, Broken: counts[id].Broken, Status: status(id)})
	}
	cfg := a.scanner.cfg
	writeJSON(w, http.StatusOK, map[string]any{
		"local": map[string]any{
			"hostDir":      cfg.PhotosHostDir,
			"containerDir": cfg.PhotosDir,
			"photos":       counts[localSourceID].OK,
			"broken":       counts[localSourceID].Broken,
			"status":       status(localSourceID),
		},
		"storages":  storages,
		"scan":      a.scanner.snapshot(),
		"scanEvery": int(cfg.ScanEvery.Seconds()),
	})
}

func (a *settingsAPI) create(w http.ResponseWriter, r *http.Request) {
	in, ok := readStorageInput(w, r)
	if !ok {
		return
	}
	in.ID = 0
	a.save(w, in.config(), http.StatusCreated)
}

func (a *settingsAPI) update(w http.ResponseWriter, r *http.Request) {
	id, ok := storageIDFromPath(w, r)
	if !ok {
		return
	}
	in, ok := readStorageInput(w, r)
	if !ok {
		return
	}
	existing, err := a.store.getStorage(id)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	c := in.config()
	c.ID = id
	if strings.TrimSpace(c.SecretKey) == "" {
		c.SecretKey = existing.SecretKey
	}
	a.save(w, c, http.StatusOK)
}

func (a *settingsAPI) save(w http.ResponseWriter, c s3Config, code int) {
	c, err := c.normalize()
	if err == nil {
		err = checkClient(c)
	}
	if err != nil {
		writeStorageError(w, err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	id, err := a.store.saveStorage(c)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	c.ID = id
	a.reload()
	slog.Info("storage saved", "id", id, "name", c.Name, "endpoint", c.Endpoint, "bucket", c.Bucket)
	writeJSON(w, code, map[string]any{"storage": storageView{s3Config: c, HasSecret: true}})
}

func (a *settingsAPI) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := storageIDFromPath(w, r)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.store.deleteStorage(id); err != nil {
		writeStorageError(w, err)
		return
	}
	a.reload()
	slog.Info("storage deleted", "id", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// test checks a storage without saving it. Editing a saved storage may leave
// the secret empty to reuse the stored one.
func (a *settingsAPI) test(w http.ResponseWriter, r *http.Request) {
	in, ok := readStorageInput(w, r)
	if !ok {
		return
	}
	c := in.config()
	if strings.TrimSpace(c.SecretKey) == "" && c.ID > 0 {
		existing, err := a.store.getStorage(c.ID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		c.SecretKey = existing.SecretKey
	}
	c, err := c.normalize()
	if err == nil {
		err = checkClient(c)
	}
	if err != nil {
		writeStorageError(w, err)
		return
	}
	src, err := newS3PhotoSource(c)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	res, err := src.probe(r.Context(), 1000)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": describeSourceError(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "images": res.Images, "checked": res.Checked, "more": res.More})
}

// reload applies the saved storages and starts a scan. Caller holds a.mu.
func (a *settingsAPI) reload() {
	list, err := a.store.listStorages()
	if err != nil {
		slog.Error("reload storages", "err", err)
		return
	}
	a.sources.useStorages(list)
	a.scanner.trigger()
}

// checkClient catches endpoints the S3 client library rejects before saving.
func checkClient(c s3Config) error {
	src, err := newS3PhotoSource(c)
	if err == nil {
		_, err = src.newClient("us-east-1")
	}
	if err != nil {
		return invalid("Endpoint 无法使用：%v", err)
	}
	return nil
}

func readStorageInput(w http.ResponseWriter, r *http.Request) (storageInput, bool) {
	var in storageInput
	// A JSON body cannot be sent by a plain cross-site form.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "请求需要 JSON"})
		return in, false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式不正确"})
		return in, false
	}
	return in, true
}

func storageIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "找不到这个对象存储"})
		return 0, false
	}
	return id, true
}

func writeStorageError(w http.ResponseWriter, err error) {
	var ue *userError
	switch {
	case errors.As(err, &ue):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": ue.msg})
	case errors.Is(err, errStorageNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "找不到这个对象存储，请刷新页面"})
	default:
		slog.Error("storage settings", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "保存失败，请查看容器日志"})
	}
}
