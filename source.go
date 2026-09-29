package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
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

func (s *sourceSet) Open(ctx context.Context, key string) (readSeekCloser, error) {
	owner := keyOwner(key)
	if owner == localSourceID {
		return s.local.Open(ctx, key)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.remotes {
		if r.ID == owner {
			return r.Src.Open(ctx, strings.TrimPrefix(key, r.Prefix))
		}
	}
	return nil, fmt.Errorf("source %s is not configured", owner)
}

type brokenSource struct{ err error }

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

// s3PhotoSource reads one bucket/prefix of any S3-compatible service.
type s3PhotoSource struct {
	cfg    s3Config
	host   string
	secure bool
	prefix string

	mu     sync.Mutex
	client *minio.Client
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
	lookup := minio.BucketLookupAuto
	switch s.cfg.Addressing {
	case addressingPath:
		lookup = minio.BucketLookupPath
	case addressingVirtual:
		lookup = minio.BucketLookupDNS
	}
	client, err := minio.New(s.host, &minio.Options{
		Creds:        credentials.NewStaticV4(s.cfg.AccessKey, s.cfg.SecretKey, ""),
		Secure:       s.secure,
		Region:       region,
		BucketLookup: lookup,
		Transport:    transport,
		MaxRetries:   2,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	return client, nil
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
		version := strings.Trim(object.ETag, `"`)
		if version == "" {
			version = fmt.Sprintf("%d:%d", object.Size, object.LastModified.UnixNano())
		}
		if err := visit(sourceObject{
			Key:     rel,
			RelPath: rel,
			Size:    object.Size,
			Mtime:   object.LastModified,
			Version: "s3:" + version,
			Backend: "s3",
		}); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (s *s3PhotoSource) Open(ctx context.Context, rel string) (readSeekCloser, error) {
	key, err := s.objectKey(rel)
	if err != nil {
		return nil, err
	}
	client, err := s.conn(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	object, err := client.GetObject(ctx, s.cfg.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		cancel()
		return nil, err
	}
	if _, err := object.Stat(); err != nil {
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
