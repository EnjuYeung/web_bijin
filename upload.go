package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/minio/minio-go/v7"
)

const uploadMaxBytes int64 = 50_000_000
const uploadTaskTTL = time.Hour

type uploadInput struct {
	Target    string `json:"target"`
	Directory string `json:"directory"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
}

type uploadTask struct {
	ID        string `json:"id"`
	Target    string `json:"target"`
	Path      string `json:"path"`
	Phase     string `json:"phase"`
	Message   string `json:"message,omitempty"`
	Saved     bool   `json:"saved"`
	Retryable bool   `json:"retryable"`
	size      int64
	kind      string
	key       string
	src       *s3PhotoSource
	created   time.Time
}

type uploadAPI struct {
	store   *store
	scanner *scanner
	sources *sourceSet
	mu      sync.Mutex
	tasks   map[string]*uploadTask
	sweepAt time.Time
}

func newUploadAPI(st *store, sc *scanner, sources *sourceSet) *uploadAPI {
	return &uploadAPI{store: st, scanner: sc, sources: sources, tasks: make(map[string]*uploadTask)}
}

func (a *uploadAPI) routes(mux *http.ServeMux, gate *authGate) {
	mux.Handle("GET /api/upload-targets", gate.protect(http.HandlerFunc(a.targets)))
	mux.Handle("POST /api/uploads", gate.protect(http.HandlerFunc(a.prepare)))
	mux.Handle("GET /api/uploads/{id}", gate.protect(http.HandlerFunc(a.status)))
	mux.Handle("PUT /api/uploads/{id}/local", gate.protect(http.HandlerFunc(a.local)))
	mux.Handle("POST /api/uploads/{id}/complete", gate.protect(http.HandlerFunc(a.complete)))
	mux.Handle("POST /api/uploads/{id}/retry", gate.protect(http.HandlerFunc(a.retry)))
}

func (a *uploadAPI) targets(w http.ResponseWriter, r *http.Request) {
	targets := []map[string]string{{"id": localSourceID, "name": "本地照片目录", "kind": "local"}}
	for _, src := range a.sources.all()[1:] {
		targets = append(targets, map[string]string{"id": src.ID, "name": src.Name, "kind": "s3"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": targets, "maxBytes": uploadMaxBytes, "maxFiles": 10000})
}

// New write endpoints use the existing session, accept same-origin browser
// requests, and never take a filesystem root or S3 credentials from the client.
func uploadOriginOK(w http.ResponseWriter, r *http.Request) bool {
	return sameOriginOK(w, r, "只能从相册网站上传")
}

func uploadJSONOK(w http.ResponseWriter, r *http.Request) bool {
	return sameOriginJSON(w, r, "只能从相册网站上传")
}

func uploadPath(in uploadInput) (string, string, error) {
	valid := func(v string) bool {
		if v == "" || !utf8.ValidString(v) || strings.ContainsAny(v, "\\\x00") || path.IsAbs(v) || path.Clean(v) != v {
			return false
		}
		for _, part := range strings.Split(v, "/") {
			if strings.HasPrefix(part, ".") || strings.ContainsAny(part, "\r\n") {
				return false
			}
		}
		return true
	}
	if !valid(in.Path) || len(strings.Split(in.Path, "/")) > 2 {
		return "", "", invalid("只允许图片或所选文件夹的直属图片，不能上传子文件夹")
	}
	if in.Directory != "" && !valid(in.Directory) {
		return "", "", invalid("目标目录必须是存储内的相对目录")
	}
	kind := map[string]string{".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp", ".gif": "image/gif"}[strings.ToLower(path.Ext(in.Path))]
	if kind == "" {
		return "", "", invalid("只支持 JPG、JPEG、PNG、WebP 和 GIF 图片")
	}
	if in.Size < 1 || in.Size > uploadMaxBytes {
		return "", "", invalid("每张图片必须大于 0 字节且不超过 50 MB")
	}
	rel := in.Path
	if in.Directory != "" {
		rel = in.Directory + "/" + rel
	}
	if len(rel) > 1024 {
		return "", "", invalid("图片路径过长")
	}
	return rel, kind, nil
}

// Check existing parents before opening a destination; os.Root still enforces
// the boundary if directory entries change after this check.
func uploadLocalParents(root *os.Root, rel string) error {
	dir := path.Dir(rel)
	if dir == "." {
		return nil
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return invalid("本地目标目录不可访问")
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return invalid("上传目录必须是真实文件夹，不能是符号链接")
		}
	}
	return nil
}

func (a *uploadAPI) prepare(w http.ResponseWriter, r *http.Request) {
	if !uploadJSONOK(w, r) {
		return
	}
	var in uploadInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "上传信息格式不正确"})
		return
	}
	rel, kind, err := uploadPath(in)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	t := &uploadTask{Target: in.Target, Path: rel, Phase: "waiting", Retryable: true, size: in.Size, kind: kind, created: time.Now()}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		writeStorageError(w, err)
		return
	}
	t.ID = hex.EncodeToString(random[:])
	if in.Target == localSourceID {
		root, err := os.OpenRoot(a.scanner.cfg.PhotosDir)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "本地照片目录不可访问，请检查目录和挂载权限"})
			return
		}
		if err := uploadLocalParents(root, rel); err != nil {
			root.Close()
			writeStorageError(w, err)
			return
		}
		_, err = root.Lstat(rel)
		root.Close()
		if err == nil {
			t.Phase, t.Message = "skipped", "已存在同名文件，已跳过"
		} else if !os.IsNotExist(err) {
			writeStorageError(w, err)
			return
		}
		t.key = rel
	} else {
		for _, src := range a.sources.all()[1:] {
			if src.ID == in.Target {
				t.src, _ = src.Src.(*s3PhotoSource)
				t.key = src.Prefix + rel
				break
			}
		}
		if t.src == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "所选对象存储不存在或不可用"})
			return
		}
		if _, err := t.src.Stat(r.Context(), rel); err == nil {
			t.Phase, t.Message = "skipped", "已存在同名文件，已跳过"
		} else if !isNotFound(err) {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": describeSourceError(err)})
			return
		}
	}
	a.mu.Lock()
	if time.Now().After(a.sweepAt) {
		for id, old := range a.tasks {
			if old.Phase != "processing" && time.Since(old.created) > uploadTaskTTL {
				delete(a.tasks, id)
			}
		}
		a.sweepAt = time.Now().Add(time.Minute)
	}
	if len(a.tasks) >= 10000 {
		for id, old := range a.tasks {
			if old.Phase == "done" || old.Phase == "skipped" {
				delete(a.tasks, id)
				break
			}
		}
	}
	if len(a.tasks) >= 10000 {
		a.mu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "上传任务过多，请等当前批次完成"})
		return
	}
	a.tasks[t.ID] = t
	a.mu.Unlock()
	if t.Phase == "skipped" {
		a.reply(w, t)
		return
	}
	headers := http.Header{"Content-Type": {kind}}
	link := "/api/uploads/" + t.ID + "/local"
	if t.src != nil {
		// Resolve the region using the existing reader. Signing uses the public
		// address and the very same configured access/secret key.
		if _, err := t.src.conn(r.Context()); err != nil {
			a.fail(t, describeSourceError(err), false)
			a.reply(w, t)
			return
		}
		client, err := t.src.newPublicClient()
		if err != nil {
			a.fail(t, describeSourceError(err), false)
			a.reply(w, t)
			return
		}
		key, err := t.src.objectKey(rel)
		if err != nil || len(key) > 1024 {
			a.fail(t, "对象路径不正确或过长", false)
			a.reply(w, t)
			return
		}
		headers.Set("If-None-Match", "*")
		headers.Set("X-Amz-Meta-Bijin-Upload", t.ID)
		signedHeaders := headers.Clone()
		signedHeaders.Set("Content-Length", fmt.Sprint(t.size))
		u, err := client.PresignHeader(r.Context(), http.MethodPut, t.src.cfg.Bucket, key, 15*time.Minute, nil, signedHeaders)
		if err != nil {
			a.fail(t, describeSourceError(err), false)
			a.reply(w, t)
			return
		}
		link = u.String()
	}
	flat := map[string]string{}
	for key := range headers {
		flat[key] = headers.Get(key)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task": a.snapshot(t), "url": link, "method": "PUT", "headers": flat})
}

func (a *uploadAPI) find(w http.ResponseWriter, r *http.Request) *uploadTask {
	a.mu.Lock()
	t := a.tasks[r.PathValue("id")]
	a.mu.Unlock()
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "上传任务已过期，请重新添加图片"})
	}
	return t
}

func (a *uploadAPI) snapshot(t *uploadTask) uploadTask {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *t
}

func (a *uploadAPI) reply(w http.ResponseWriter, t *uploadTask) {
	writeJSON(w, http.StatusOK, map[string]any{"task": a.snapshot(t)})
}

func (a *uploadAPI) fail(t *uploadTask, message string, retryable bool) {
	a.mu.Lock()
	t.Phase, t.Message, t.Retryable = "error", message, retryable
	a.mu.Unlock()
}

func (a *uploadAPI) status(w http.ResponseWriter, r *http.Request) {
	if t := a.find(w, r); t != nil {
		a.reply(w, t)
	}
}

func (a *uploadAPI) local(w http.ResponseWriter, r *http.Request) {
	if !uploadOriginOK(w, r) {
		return
	}
	t := a.find(w, r)
	if t == nil {
		return
	}
	a.mu.Lock()
	if t.Target != localSourceID || t.Phase != "waiting" {
		a.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "该任务不能再次写入"})
		return
	}
	t.Phase = "uploading"
	a.mu.Unlock()
	fail := func(code int, message string) {
		a.fail(t, message, false)
		writeJSON(w, code, map[string]string{"error": message})
	}
	if r.ContentLength != t.size {
		fail(http.StatusBadRequest, "图片大小与准备上传时不一致")
		return
	}
	root, err := os.OpenRoot(a.scanner.cfg.PhotosDir)
	if err != nil {
		fail(http.StatusInternalServerError, "本地照片目录不可写")
		return
	}
	defer root.Close()
	dir := path.Dir(t.Path)
	if dir != "." {
		if err := uploadLocalParents(root, t.Path); err != nil {
			fail(http.StatusBadRequest, err.Error())
			return
		}
		if err := root.MkdirAll(dir, 0755); err != nil {
			fail(http.StatusInternalServerError, "无法创建本地目录，请检查写入权限")
			return
		}
	}
	tmp := ".bijin-upload-" + t.ID
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		fail(http.StatusInternalServerError, "无法写入本地目录，请检查只读挂载和目录权限")
		return
	}
	defer root.Remove(tmp)
	defer f.Close()
	n, err := io.Copy(f, http.MaxBytesReader(w, r.Body, t.size+1))
	if err != nil || n != t.size {
		fail(http.StatusBadRequest, "图片没有完整传输，请重新上传")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		fail(http.StatusInternalServerError, "无法校验上传图片")
		return
	}
	info, format, err := image.DecodeConfig(f)
	if err != nil || "image/"+format != t.kind || info.Width < 1 || info.Height < 1 || int64(info.Height) > a.scanner.cfg.MaxPixels/int64(max(info.Width, 1)) {
		fail(http.StatusBadRequest, "文件不是可显示的图片，或图片像素超过上限")
		return
	}
	if err := f.Sync(); err != nil {
		fail(http.StatusInternalServerError, "图片写入失败，请检查可用磁盘空间")
		return
	}
	// Linking the complete temporary file is atomic and refuses an existing
	// destination. Rename would silently overwrite on Linux.
	if err := root.Link(tmp, t.Path); err != nil {
		if os.IsExist(err) {
			a.mu.Lock()
			t.Phase, t.Message = "skipped", "已存在同名文件，已跳过"
			a.mu.Unlock()
			a.reply(w, t)
		} else {
			fail(http.StatusInternalServerError, "无法保存图片")
		}
		return
	}
	a.start(t)
	a.reply(w, t)
}

func (a *uploadAPI) complete(w http.ResponseWriter, r *http.Request) {
	if !uploadJSONOK(w, r) {
		return
	}
	t := a.find(w, r)
	if t == nil {
		return
	}
	current := a.snapshot(t)
	if current.Saved || current.Phase == "skipped" {
		a.reply(w, t)
		return
	}
	if t.src == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "本地图片尚未保存"})
		return
	}
	client, err := t.src.conn(r.Context())
	if err != nil {
		writeStorageError(w, err)
		return
	}
	key, _ := t.src.objectKey(t.Path)
	object, err := client.StatObject(r.Context(), t.src.cfg.Bucket, key, minio.StatObjectOptions{})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "尚未确认对象保存成功：" + describeSourceError(err)})
		return
	}
	if object.Metadata.Get("X-Amz-Meta-Bijin-Upload") != t.ID {
		a.mu.Lock()
		t.Phase, t.Message = "skipped", "已存在同名文件，已跳过"
		a.mu.Unlock()
		a.reply(w, t)
		return
	}
	if object.Size != t.size {
		a.fail(t, "存储中的图片大小与上传记录不一致", false)
		a.reply(w, t)
		return
	}
	a.start(t)
	a.reply(w, t)
}

func (a *uploadAPI) start(t *uploadTask) {
	a.mu.Lock()
	if t.Phase == "processing" || t.Phase == "done" {
		a.mu.Unlock()
		return
	}
	t.Saved, t.Phase, t.Message, t.Retryable = true, "processing", "", true
	a.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		result := a.scanner.photos.SyncKey(ctx, t.key)
		err := result.Err
		if err == nil && (!result.AlbumUsable || !result.WallpapersReady) {
			switch {
			case result.Invalid:
				err = fmt.Errorf("图片无法完整解码，未进入图库；请检查原文件")
			case result.Missing:
				err = fmt.Errorf("保存的图片已不存在")
			default:
				err = fmt.Errorf("图片处理尚未完成")
				result.Retryable = true
			}
		}
		if err != nil {
			a.fail(t, "图片已保存，图库处理失败："+describeSourceError(err), result.Retryable)
			return
		}
		a.mu.Lock()
		t.Phase, t.Message = "done", "图片已进入图库"
		a.mu.Unlock()
	}()
}

func (a *uploadAPI) retry(w http.ResponseWriter, r *http.Request) {
	if !uploadJSONOK(w, r) {
		return
	}
	if t := a.find(w, r); t != nil {
		current := a.snapshot(t)
		if current.Phase != "error" || !current.Saved || !current.Retryable {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "该任务不能重试处理"})
			return
		}
		a.start(t)
		a.reply(w, t)
	}
}
