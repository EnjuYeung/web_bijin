package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

type shareAPI struct {
	store   *store
	sources *sourceSet
	tempDir string
	gate    chan struct{}
}

func newShareAPI(st *store, sources *sourceSet, tempDir string) *shareAPI {
	return &shareAPI{store: st, sources: sources, tempDir: tempDir, gate: make(chan struct{}, 2)}
}

func (a *shareAPI) routes(mux *http.ServeMux, gate *authGate) {
	mux.Handle("GET /api/photos/{id}/share", gate.protect(http.HandlerFunc(a.get)))
	mux.Handle("POST /api/photos/{id}/share", gate.protectWrite("请在相册页面里分享图片", true, a.create))
	mux.Handle("DELETE /api/photos/{id}/share", gate.protectWrite("请在相册页面里停止分享", true, a.stop))
	mux.HandleFunc("GET /i/{name}", a.media)
	mux.HandleFunc("OPTIONS /i/{name}", handleWallpaperPreflight)
}

func photoShareVersion(p photo) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s", p.ID, p.sourceKey(), photoVersion(p))))
	return hex.EncodeToString(digest[:])
}

func (a *shareAPI) state(w http.ResponseWriter, p photo, sh photoShare) {
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"active": sh.Token != "", "generation": sh.Generation,
		"version": photoShareVersion(p), "path": sh.path()})
}

func (a *shareAPI) get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := photoFromReq(w, r, a.store)
	if !ok {
		return
	}
	sh, err := a.store.shareForPhoto(p.ID)
	if err != nil {
		a.failure(w, r, p.ID, err)
		return
	}
	if sh.Token != "" {
		scope, scopeErr := a.scope(p.sourceKey())
		object, statErr := a.sources.Stat(r.Context(), p.sourceKey())
		if errors.Is(scopeErr, errSourceNotConfigured) || errors.Is(statErr, errSourceNotConfigured) || isNotFound(statErr) ||
			(scopeErr == nil && statErr == nil && (scope != sh.Scope || object.Version != sh.SourceVersion || object.Size != sh.Size)) {
			if err := a.store.retireShare(sh.Token); err != nil {
				a.failure(w, r, p.ID, err)
				return
			}
			sh, err = a.store.shareForPhoto(p.ID)
			if err != nil {
				a.failure(w, r, p.ID, err)
				return
			}
		}
	}
	a.state(w, p, sh)
}

type shareWrite struct {
	Version    string `json:"version"`
	Generation *int64 `json:"generation"`
}

func readShareWrite(w http.ResponseWriter, r *http.Request) (shareWrite, bool) {
	var body shareWrite
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&body)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("extra body")
		}
	}
	if err != nil || body.Generation == nil || *body.Generation < 0 || len(body.Version) != 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "分享请求不完整，请重新打开图片"})
		return body, false
	}
	return body, true
}

func (a *shareAPI) selected(w http.ResponseWriter, r *http.Request, body shareWrite) (photo, bool) {
	p, ok := photoFromReq(w, r, a.store)
	if !ok {
		return p, false
	}
	if !photoVisible(p) || photoShareVersion(p) != body.Version {
		a.failure(w, r, p.ID, errSourceChanged)
		return p, false
	}
	return p, true
}

func (a *shareAPI) acquire(ctx context.Context) error {
	select {
	case a.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-a.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *shareAPI) create(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	body, ok := readShareWrite(w, r)
	if !ok {
		return
	}
	p, ok := a.selected(w, r, body)
	if !ok {
		return
	}
	if err := a.acquire(r.Context()); err != nil {
		return
	}
	defer func() { <-a.gate }()
	snapshot, err := a.snapshot(r.Context(), p)
	if err != nil {
		if shareSourceChanged(err) {
			sh, _ := a.store.shareForPhoto(p.ID)
			if sh.Token != "" {
				if retireErr := a.store.retireShare(sh.Token); retireErr != nil {
					err = retireErr
				}
			}
		}
		a.failure(w, r, p.ID, err)
		return
	}
	defer snapshot.file.Close()
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		a.failure(w, r, p.ID, err)
		return
	}
	sh, err := a.store.saveShare(p, *body.Generation, photoShare{
		Token: base64.RawURLEncoding.EncodeToString(token[:]), SourceKey: p.sourceKey(), SourceVersion: p.SourceVersion,
		Scope: snapshot.scope, Digest: snapshot.digest, MIME: snapshot.mime, Extension: snapshot.extension, Size: snapshot.size,
	})
	if err != nil {
		a.failure(w, r, p.ID, err)
		return
	}
	a.state(w, p, sh)
}

func (a *shareAPI) stop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	body, ok := readShareWrite(w, r)
	if !ok {
		return
	}
	p, ok := a.selected(w, r, body)
	if !ok {
		return
	}
	sh, err := a.store.stopShare(p.ID, *body.Generation)
	if err != nil {
		a.failure(w, r, p.ID, err)
		return
	}
	a.state(w, p, sh)
}

func shareSourceChanged(err error) bool {
	return errors.Is(err, errSourceChanged) || errors.Is(err, errSourceNotConfigured) || isNotFound(err) || minio.ToErrorResponse(err).StatusCode == http.StatusPreconditionFailed
}

func (a *shareAPI) failure(w http.ResponseWriter, r *http.Request, id int64, err error) {
	w.Header().Set("Cache-Control", "no-store")
	code, message := http.StatusBadGateway, "暂时无法读取原图，请稍后重试"
	if errors.Is(err, errShareStateChanged) {
		code, message = http.StatusConflict, "分享状态已改变，请重新核对后操作"
	} else if shareSourceChanged(err) {
		code, message = http.StatusConflict, "原图已改变或移走，请刷新图片后重新分享"
	} else if r.Context().Err() == nil {
		slog.Warn("share original", "id", id, "err", err)
	}
	writeJSON(w, code, map[string]string{"error": message})
}

// Scope survives restarts, but changes when a source is pointed elsewhere.
func (a *shareAPI) scope(key string) (string, error) {
	source, _, err := a.sources.resolve(key)
	if err != nil {
		return "", err
	}
	identity := keyOwner(key)
	switch source := source.(type) {
	case *localPhotoSource:
		identity += ":" + source.root
	case *s3PhotoSource:
		identity += ":" + source.cfg.Endpoint + ":" + source.cfg.Bucket + ":" + source.cfg.Prefix
	default:
		identity += fmt.Sprintf(":%T", source) // controlled test sources
	}
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:]), nil
}

type shareSnapshot struct {
	file                           *os.File
	digest, scope, mime, extension string
	size                           int64
}

type shareReader struct {
	context.Context
	io.Reader
}

func (r shareReader) Read(b []byte) (int, error) {
	if err := r.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(b)
}

// Hash and serve the same anonymous file. Reopening the original after a hash
// would let an in-place writer substitute bytes between validation and output.
func (a *shareAPI) snapshot(ctx context.Context, p photo) (*shareSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	defer cancel()
	scope, err := a.scope(p.sourceKey())
	if err != nil {
		return nil, err
	}
	object, err := a.sources.Stat(ctx, p.sourceKey())
	if err != nil {
		return nil, err
	}
	if object.Version != p.SourceVersion || object.Size != p.Size || object.Size <= 0 {
		return nil, errSourceChanged
	}
	source, err := a.sources.OpenVersion(ctx, object)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	if err := os.MkdirAll(a.tempDir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(a.tempDir, ".original-*")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(file.Name()); err != nil {
		file.Close()
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			file.Close()
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), shareReader{ctx, io.LimitReader(source, object.Size+1)})
	if err != nil {
		return nil, err
	}
	if n != object.Size {
		return nil, errSourceChanged
	}
	current, err := a.sources.Stat(ctx, p.sourceKey())
	if err != nil {
		return nil, err
	}
	if current.Version != object.Version || current.Size != object.Size {
		return nil, errSourceChanged
	}
	currentScope, err := a.scope(p.sourceKey())
	if err != nil {
		return nil, err
	}
	if currentScope != scope {
		return nil, errSourceChanged
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	_, format, err := image.DecodeConfig(file)
	if err != nil {
		return nil, errSourceChanged
	}
	extensions := map[string]string{"jpeg": "jpg", "png": "png", "gif": "gif", "webp": "webp"}
	extension, ok := extensions[format]
	if !ok {
		return nil, errSourceChanged
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keep = true
	return &shareSnapshot{file: file, digest: hex.EncodeToString(hash.Sum(nil)), scope: scope, mime: "image/" + format, extension: extension, size: n}, nil
}

func (a *shareAPI) media(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	name := r.PathValue("name")
	token, extension, found := strings.Cut(name, ".")
	if !found || len(token) != base64.RawURLEncoding.EncodedLen(32) {
		http.NotFound(w, r)
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if !found || err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		http.NotFound(w, r)
		return
	}
	sh, err := a.store.shareForToken(token)
	if err != nil {
		http.Error(w, "image unavailable", http.StatusServiceUnavailable)
		return
	}
	if sh.Token == "" || sh.Extension != extension {
		http.NotFound(w, r)
		return
	}
	p, ok, err := a.store.getByID(sh.PhotoID)
	if err != nil {
		http.Error(w, "image unavailable", http.StatusServiceUnavailable)
		return
	}
	if !ok || !photoVisible(p) || p.sourceKey() != sh.SourceKey || p.SourceVersion != sh.SourceVersion {
		a.retireMedia(w, r, sh)
		return
	}
	if err := a.acquire(r.Context()); err != nil {
		return
	}
	defer func() { <-a.gate }()
	snapshot, err := a.snapshot(r.Context(), p)
	if err != nil {
		if shareSourceChanged(err) {
			a.retireMedia(w, r, sh)
		} else {
			http.Error(w, "image unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	defer snapshot.file.Close()
	if snapshot.digest != sh.Digest || snapshot.scope != sh.Scope || snapshot.size != sh.Size || snapshot.mime != sh.MIME {
		a.retireMedia(w, r, sh)
		return
	}
	// Stop/deletion may have happened during the source read; check once more
	// before any HEAD, Range, conditional response or original bytes are sent.
	current, err := a.store.shareForToken(token)
	if err != nil {
		http.Error(w, "image unavailable", http.StatusServiceUnavailable)
		return
	}
	if current.Token == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", sh.MIME)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("ETag", `"`+sh.Digest+`"`)
	http.ServeContent(w, r, "image."+sh.Extension, time.Time{}, snapshot.file)
}

func (a *shareAPI) retireMedia(w http.ResponseWriter, r *http.Request, sh photoShare) {
	if err := a.store.retireShare(sh.Token); err != nil {
		http.Error(w, "image unavailable", http.StatusServiceUnavailable)
		return
	}
	http.NotFound(w, r)
}
