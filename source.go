package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type sourceObject struct {
	Key     string
	RelPath string
	Size    int64
	Mtime   time.Time
	Version string
	Backend string
	ETag    string // source metadata for conditional S3 reads; not persisted
}

type readSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

type imageOpener interface {
	Open(context.Context, string) (readSeekCloser, error)
}

type photoSource interface {
	imageOpener
	Stat(context.Context, string) (sourceObject, error)
	Walk(context.Context, func(sourceObject) error) error
}

// Local photos keep their relative path as index key. Each object storage uses
// its own key namespace, so equal relative paths from different sources become
// separate photos while sharing the same display path and album.
const (
	localSourceID = "local"
	sourceKeyRoot = ".bijin-source/"
)

func storageSourceID(id int64) string { return "s3-" + strconv.FormatInt(id, 10) }

func storageKeyPrefix(id int64) string { return sourceKeyRoot + storageSourceID(id) + "/" }

// keyOwner returns the source ID that owns an index key.
func keyOwner(key string) string {
	rest, ok := strings.CutPrefix(key, sourceKeyRoot)
	if !ok {
		return localSourceID
	}
	owner, _, _ := strings.Cut(rest, "/")
	return owner
}

type namedSource struct {
	ID     string
	Name   string
	Prefix string
	Src    photoSource
	config s3Config
}

// sourceSet is the local directory plus the storages configured on the
// settings page. Storages can be replaced while the app is running.
type sourceSet struct {
	local   photoSource
	mu      sync.RWMutex
	remotes []namedSource
}

func newSourceSet(local photoSource) *sourceSet {
	return &sourceSet{local: local}
}

func (s *sourceSet) all() []namedSource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]namedSource, 0, len(s.remotes)+1)
	out = append(out, namedSource{ID: localSourceID, Name: "本地图库", Src: s.local})
	return append(out, s.remotes...)
}

func (s *sourceSet) setRemotes(list []namedSource) {
	s.mu.Lock()
	s.remotes = list
	s.mu.Unlock()
}

// useStorages rebuilds object storage sources, reusing unchanged ones so their
// connections and detected region survive unrelated edits.
func (s *sourceSet) useStorages(list []s3Config) {
	s.mu.RLock()
	existing := make(map[int64]namedSource, len(s.remotes))
	for _, r := range s.remotes {
		existing[r.config.ID] = r
	}
	s.mu.RUnlock()
	remotes := make([]namedSource, 0, len(list))
	for _, c := range list {
		if old, ok := existing[c.ID]; ok && old.config == c {
			remotes = append(remotes, old)
			continue
		}
		var src photoSource
		if s3src, err := newS3PhotoSource(c); err == nil {
			src = s3src
		} else {
			// Keep the source registered so its photos stay indexed and the
			// error is shown, instead of being treated as a removed storage.
			slog.Error("storage", "id", c.ID, "name", c.Name, "err", err)
			src = brokenSource{err}
		}
		remotes = append(remotes, namedSource{ID: storageSourceID(c.ID), Name: c.Name, Prefix: storageKeyPrefix(c.ID), Src: src, config: c})
	}
	s.setRemotes(remotes)
}

var errSourceNotConfigured = errors.New("photo source is not configured")

func (s *sourceSet) resolve(key string) (photoSource, string, error) {
	owner := keyOwner(key)
	if owner == localSourceID {
		return s.local, key, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.remotes {
		if r.ID == owner {
			return r.Src, strings.TrimPrefix(key, r.Prefix), nil
		}
	}
	return nil, "", fmt.Errorf("%w: %s", errSourceNotConfigured, owner)
}

func (s *sourceSet) Open(ctx context.Context, key string) (readSeekCloser, error) {
	source, rel, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	return source.Open(ctx, rel)
}

func (s *sourceSet) Stat(ctx context.Context, key string) (sourceObject, error) {
	ctx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	defer cancel()
	source, rel, err := s.resolve(key)
	if err != nil {
		return sourceObject{}, err
	}
	object, err := source.Stat(ctx, rel)
	object.Key = key
	return object, err
}

func (s *sourceSet) OpenVersion(ctx context.Context, object sourceObject) (readSeekCloser, error) {
	source, rel, err := s.resolve(object.Key)
	if err != nil {
		return nil, err
	}
	if opener, ok := source.(interface {
		OpenVersion(context.Context, sourceObject) (readSeekCloser, error)
	}); ok {
		object.Key, object.RelPath = rel, rel
		return opener.OpenVersion(ctx, object)
	}
	return source.Open(ctx, rel)
}

// originalLink returns a presigned browser URL for an original when the
// photo's storage allows direct originals; ok is false otherwise.
func (s *sourceSet) originalLink(ctx context.Context, key, version string) (link string, ok bool, err error) {
	owner := keyOwner(key)
	if owner == localSourceID {
		return "", false, nil
	}
	s.mu.RLock()
	var src *s3PhotoSource
	var prefix string
	for _, r := range s.remotes {
		if r.ID == owner {
			src, _ = r.Src.(*s3PhotoSource)
			prefix = r.Prefix
			break
		}
	}
	s.mu.RUnlock()
	if src == nil || !src.cfg.DirectOriginal {
		return "", false, nil
	}
	link, err = src.Presign(ctx, strings.TrimPrefix(key, prefix), version)
	return link, err == nil, err
}

// eventTarget is one storage key named by a storage notification.
type eventTarget struct {
	source string
	src    *s3PhotoSource
	rel    string
	key    string // index key
}

// match finds the storages a notification for bucket/objectKey can belong to.
// Bucket names are only unique per service, so every storage with that bucket
// and prefix is later checked against its own service before anything changes.
func (s *sourceSet) match(bucket, objectKey string) []eventTarget {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []eventTarget
	for _, r := range s.remotes {
		src, ok := r.Src.(*s3PhotoSource)
		if !ok || src.cfg.Bucket != bucket {
			continue
		}
		rel, ok := src.relativeKey(objectKey)
		if !ok || hiddenRel(rel) || !isImageName(path.Base(rel)) {
			continue
		}
		out = append(out, eventTarget{source: r.ID, src: src, rel: rel, key: r.Prefix + rel})
	}
	return out
}

type brokenSource struct{ err error }

func (b brokenSource) Stat(context.Context, string) (sourceObject, error) {
	return sourceObject{}, b.err
}

func (b brokenSource) Walk(context.Context, func(sourceObject) error) error { return b.err }
func (b brokenSource) Open(context.Context, string) (readSeekCloser, error) {
	return nil, b.err
}

type localPhotoSource struct {
	root string
}

func (s *localPhotoSource) Walk(ctx context.Context, visit func(sourceObject) error) error {
	st, err := os.Stat(s.root)
	if err != nil {
		return fmt.Errorf("photos dir: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("photos dir is not a directory: %s", s.root)
	}
	return filepath.WalkDir(s.root, func(filePath string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return walkErr
		}
		name := d.Name()
		if name != "." && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !isImageName(name) {
			return nil
		}
		rel, err := filepath.Rel(s.root, filePath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if hiddenRel(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return visit(sourceObject{
			Key:     rel,
			RelPath: rel,
			Size:    info.Size(),
			Mtime:   info.ModTime(),
			Version: fmt.Sprintf("local:%d:%d", info.Size(), info.ModTime().UnixNano()),
			Backend: "local",
		})
	})
}

func (s *localPhotoSource) Open(_ context.Context, rel string) (readSeekCloser, error) {
	full, err := safeRelPath(s.root, filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

func localObject(rel string, info os.FileInfo) sourceObject {
	return sourceObject{Key: rel, RelPath: rel, Size: info.Size(), Mtime: info.ModTime(),
		Version: fmt.Sprintf("local:%d:%d", info.Size(), info.ModTime().UnixNano()), Backend: "local"}
}

func (s *localPhotoSource) Stat(ctx context.Context, rel string) (sourceObject, error) {
	if err := ctx.Err(); err != nil {
		return sourceObject{}, err
	}
	full, err := safeRelPath(s.root, filepath.FromSlash(rel))
	if err != nil {
		return sourceObject{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return sourceObject{}, err
	}
	if !info.Mode().IsRegular() {
		return sourceObject{}, fmt.Errorf("photo is not a regular file")
	}
	return localObject(rel, info), nil
}

func (s *localPhotoSource) OpenVersion(ctx context.Context, object sourceObject) (readSeekCloser, error) {
	file, err := s.Open(ctx, object.RelPath)
	if err != nil {
		return nil, err
	}
	info, err := file.(*os.File).Stat()
	if err == nil && localObject(object.RelPath, info).Version != object.Version {
		err = errSourceChanged
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// s3PhotoSource reads one bucket/prefix of any S3-compatible service.
type s3PhotoSource struct {
	cfg    s3Config
	host   string
	secure bool
	prefix string

	mu        sync.Mutex
	client    *minio.Client
	region    string
	presigner *minio.Client // public endpoint; signing needs no request
	links     map[string]presignedLink
}

// A presigned original is valid for presignTTL and handed out again for
// presignReuse, so reopening a photo hits the browser cache.
const (
	presignTTL   = 24 * time.Hour
	presignReuse = 12 * time.Hour
)

type presignedLink struct {
	url     string
	expires time.Time
}

func newS3PhotoSource(c s3Config) (*s3PhotoSource, error) {
	endpoint, err := normalizeEndpoint(c.Endpoint)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint)
	prefix, err := cleanObjectPrefix(c.Prefix)
	if err != nil {
		return nil, fmt.Errorf("prefix: %w", err)
	}
	return &s3PhotoSource{cfg: c, host: u.Host, secure: u.Scheme == "https", prefix: prefix}, nil
}

func (s *s3PhotoSource) newClient(region string) (*minio.Client, error) {
	transport, err := minio.DefaultTransport(s.secure)
	if err != nil {
		return nil, fmt.Errorf("create S3 transport: %w", err)
	}
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.TLSHandshakeTimeout = 5 * time.Second
	client, err := minio.New(s.host, &minio.Options{
		Creds:        credentials.NewStaticV4(s.cfg.AccessKey, s.cfg.SecretKey, ""),
		Secure:       s.secure,
		Region:       region,
		BucketLookup: s.bucketLookup(),
		Transport:    transport,
		MaxRetries:   2,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	return client, nil
}

func (s *s3PhotoSource) bucketLookup() minio.BucketLookupType {
	switch s.cfg.Addressing {
	case addressingPath:
		return minio.BucketLookupPath
	case addressingVirtual:
		return minio.BucketLookupDNS
	}
	return minio.BucketLookupAuto
}

// newPublicClient signs URLs for the address browsers use. The server may
// reach the same service on an internal address, and a SigV4 signature is
// bound to the host, so the two clients are kept apart.
func (s *s3PhotoSource) newPublicClient() (*minio.Client, error) {
	raw := s.cfg.PublicEndpoint
	if raw == "" {
		raw = s.cfg.Endpoint
	}
	endpoint, err := normalizeEndpoint(raw)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint)
	return minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(s.cfg.AccessKey, s.cfg.SecretKey, ""),
		Secure:       u.Scheme == "https",
		Region:       s.region,
		BucketLookup: s.bucketLookup(),
	})
}

// conn returns the client, detecting the bucket region first when none was
// configured. A service that answers the lookup with a client error or
// NotImplemented gets us-east-1, which S3-compatible services generally
// accept; network errors and temporary 5xx failures are retried next time.
func (s *s3PhotoSource) conn(ctx context.Context) (*minio.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	region := s.cfg.Region
	if region == "" {
		probe, err := s.newClient("")
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		region, err = probe.GetBucketLocation(ctx, s.cfg.Bucket)
		if err != nil {
			var resp minio.ErrorResponse
			if !errors.As(err, &resp) || (resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented) {
				return nil, err
			}
			region = "us-east-1"
		}
	}
	var err error
	if s.client, err = s.newClient(region); err != nil {
		return nil, err
	}
	s.region = region
	return s.client, nil
}

func (s *s3PhotoSource) Walk(ctx context.Context, visit func(sourceObject) error) error {
	client, err := s.conn(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	objects := client.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{
		Prefix:    s.prefix,
		Recursive: true,
		UseV1:     s.cfg.ListV1,
	})
	for object := range objects {
		if object.Err != nil {
			return object.Err
		}
		rel, ok := s.relativeKey(object.Key)
		if !ok || hiddenRel(rel) || !isImageName(path.Base(rel)) {
			continue
		}
		if err := visit(s3Object(rel, object.ETag, object.Size, object.LastModified)); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// s3Object builds the index view of one object; listing and Stat must agree
// on the version, or a notification would reprocess an unchanged photo.
func s3Object(rel, etag string, size int64, mtime time.Time) sourceObject {
	version := strings.Trim(etag, `"`)
	if version == "" {
		version = fmt.Sprintf("%d:%d", size, mtime.UnixNano())
	}
	return sourceObject{Key: rel, RelPath: rel, Size: size, Mtime: mtime, Version: "s3:" + version, Backend: "s3", ETag: strings.Trim(etag, `"`)}
}

// Stat reads the current state of one object; a notification only names it.
func (s *s3PhotoSource) Stat(ctx context.Context, rel string) (sourceObject, error) {
	key, err := s.objectKey(rel)
	if err != nil {
		return sourceObject{}, err
	}
	client, err := s.conn(ctx)
	if err != nil {
		return sourceObject{}, err
	}
	info, err := client.StatObject(ctx, s.cfg.Bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return sourceObject{}, err
	}
	return s3Object(rel, info.ETag, info.Size, info.LastModified), nil
}

func isNotFound(err error) bool {
	var resp minio.ErrorResponse
	return errors.Is(err, os.ErrNotExist) || errors.As(err, &resp) && (resp.StatusCode == http.StatusNotFound || resp.Code == "NoSuchKey")
}

// Presign returns a browser URL for the original of one object version. The
// same URL is reused for presignReuse, and the response tells the browser to
// keep it that long; a new version gets a new URL, so nothing stale is shown.
func (s *s3PhotoSource) Presign(ctx context.Context, rel, version string) (string, error) {
	key, err := s.objectKey(rel)
	if err != nil {
		return "", err
	}
	if _, err := s.conn(ctx); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	id := rel + "\n" + version
	if l, ok := s.links[id]; ok && l.expires.Sub(now) > presignTTL-presignReuse {
		return l.url, nil
	}
	if s.presigner == nil {
		if s.presigner, err = s.newPublicClient(); err != nil {
			return "", err
		}
	}
	params := url.Values{"response-cache-control": {fmt.Sprintf("private, max-age=%d, immutable", int(presignReuse/time.Second))}}
	if kind := mime.TypeByExtension(strings.ToLower(path.Ext(rel))); kind != "" {
		params.Set("response-content-type", kind)
	}
	u, err := s.presigner.PresignedGetObject(ctx, s.cfg.Bucket, key, presignTTL, params)
	if err != nil {
		return "", err
	}
	if len(s.links) >= 4096 {
		for k, l := range s.links {
			if l.expires.Sub(now) <= presignTTL-presignReuse {
				delete(s.links, k)
			}
		}
	}
	if s.links == nil {
		s.links = map[string]presignedLink{}
	}
	s.links[id] = presignedLink{url: u.String(), expires: now.Add(presignTTL)}
	return u.String(), nil
}

func (s *s3PhotoSource) Open(ctx context.Context, rel string) (readSeekCloser, error) {
	return s.open(ctx, rel, "", "")
}

func (s *s3PhotoSource) OpenVersion(ctx context.Context, object sourceObject) (readSeekCloser, error) {
	return s.open(ctx, object.RelPath, object.Version, object.ETag)
}

func (s *s3PhotoSource) open(ctx context.Context, rel, version, etag string) (readSeekCloser, error) {
	key, err := s.objectKey(rel)
	if err != nil {
		return nil, err
	}
	client, err := s.conn(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	options := minio.GetObjectOptions{}
	if etag != "" {
		if err := options.SetMatchETag(etag); err != nil {
			cancel()
			return nil, err
		}
	}
	object, err := client.GetObject(ctx, s.cfg.Bucket, key, options)
	if err != nil {
		cancel()
		return nil, err
	}
	info, err := object.Stat()
	if err == nil && version != "" && s3Object(rel, info.ETag, info.Size, info.LastModified).Version != version {
		err = errSourceChanged
	}
	if err != nil {
		_ = object.Close()
		cancel()
		return nil, err
	}
	return &cancelReader{readSeekCloser: object, cancel: cancel}, nil
}

type probeResult struct {
	Images  int  `json:"images"`
	Checked int  `json:"checked"`
	More    bool `json:"more"`
}

// probe lists up to limit objects and reads the metadata of the first image,
// proving both list and read permission without downloading photos.
func (s *s3PhotoSource) probe(ctx context.Context, limit int) (probeResult, error) {
	var res probeResult
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, err := s.conn(ctx)
	if err != nil {
		return res, err
	}
	var firstImage string
	for object := range client.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{Prefix: s.prefix, Recursive: true, UseV1: s.cfg.ListV1}) {
		if object.Err != nil {
			return res, object.Err
		}
		if res.Checked == limit {
			res.More = true
			break
		}
		res.Checked++
		rel, ok := s.relativeKey(object.Key)
		if ok && !hiddenRel(rel) && isImageName(path.Base(rel)) {
			res.Images++
			if firstImage == "" {
				firstImage = object.Key
			}
		}
	}
	if firstImage != "" {
		if _, err := client.StatObject(ctx, s.cfg.Bucket, firstImage, minio.StatObjectOptions{}); err != nil {
			return res, fmt.Errorf("能列出文件但读不到图片：%w", err)
		}
	}
	return res, nil
}

func (s *s3PhotoSource) objectKey(rel string) (string, error) {
	if rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("bad object path")
	}
	return s.prefix + rel, nil
}

func (s *s3PhotoSource) relativeKey(key string) (string, bool) {
	if !strings.HasPrefix(key, s.prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(key, s.prefix)
	if _, err := s.objectKey(rel); err != nil {
		return "", false
	}
	return rel, true
}

func cleanObjectPrefix(raw string) (string, error) {
	prefix := strings.Trim(strings.TrimSpace(raw), "/")
	if prefix == "" {
		return "", nil
	}
	if path.Clean(prefix) != prefix || prefix == ".." || strings.HasPrefix(prefix, "../") {
		return "", fmt.Errorf("bad prefix")
	}
	return prefix + "/", nil
}

// describeSourceError turns storage failures into a short Chinese hint for
// the settings page while keeping the service's own code for diagnosis.
func describeSourceError(err error) string {
	if err == nil {
		return ""
	}
	var resp minio.ErrorResponse
	errors.As(err, &resp)
	hint := ""
	switch resp.Code {
	case "":
	case "AccessDenied":
		hint = "没有权限：确认密钥可以列出和读取这个 Bucket"
	case "InvalidAccessKeyId":
		hint = "Access Key 不存在"
	case "SignatureDoesNotMatch":
		hint = "Secret Key 不正确，或 Region 与服务不一致"
	case "NoSuchBucket":
		hint = "Bucket 不存在"
	case "AuthorizationHeaderMalformed", "InvalidRegion", "IllegalLocationConstraintException":
		hint = "Region 不正确"
		if resp.Region != "" {
			hint += "，服务要求 " + resp.Region
		}
	case "PermanentRedirect", "TemporaryRedirect":
		hint = "Endpoint 与 Bucket 所在区域不一致，或需要换一种寻址方式"
	case "NotImplemented":
		hint = "服务不支持这个请求：可在高级设置中改用旧版列举接口或换一种寻址方式"
	default:
		hint = "存储服务返回错误 " + resp.Code
	}
	msg := err.Error()
	var netErr net.Error
	switch {
	case hint != "":
		return hint + "（" + msg + "）"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return "连接超时：确认 Endpoint 能从服务器访问（" + msg + "）"
	case strings.Contains(msg, "HTTP response to HTTPS client"):
		return "这个 Endpoint 是 http 服务：请在地址前写 http://（" + msg + "）"
	case strings.Contains(msg, "x509:") || strings.Contains(msg, "tls:"):
		return "HTTPS 证书校验失败（" + msg + "）"
	case strings.Contains(msg, "no such host"):
		return "找不到这个域名：检查 Endpoint，或寻址方式是否应改为路径（" + msg + "）"
	case strings.Contains(msg, "Connection closed by foreign host"):
		return "连接被对方关闭：检查 Endpoint 的地址、端口和协议（" + msg + "）"
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "dial tcp"):
		return "连不上 Endpoint：检查地址、端口和协议（" + msg + "）"
	}
	return msg
}

type cancelReader struct {
	readSeekCloser
	cancel context.CancelFunc
}

func (r *cancelReader) Close() error { r.cancel(); return r.readSeekCloser.Close() }

func safeRelPath(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("bad path")
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bad path")
	}
	full := filepath.Join(root, clean)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	sep := string(filepath.Separator)
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+sep) {
		return "", fmt.Errorf("outside root")
	}
	return absFull, nil
}
