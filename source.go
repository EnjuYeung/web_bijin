package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
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

type photoSource interface {
	Walk(context.Context, func(sourceObject) error) error
	Open(context.Context, string) (readSeekCloser, error)
	Name() string
}

func newPhotoSource(cfg config) (photoSource, error) {
	local := &localPhotoSource{root: cfg.PhotosDir}
	switch cfg.StorageBackend {
	case "local":
		return local, nil
	case "s3":
		s3, err := newS3PhotoSource(cfg)
		if err != nil {
			return nil, err
		}
		return &combinedPhotoSource{local: local, s3: s3}, nil
	default:
		return nil, fmt.Errorf("unsupported STORAGE_BACKEND %q", cfg.StorageBackend)
	}
}

type localPhotoSource struct {
	root string
}

func (s *localPhotoSource) Name() string { return "local" }

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

type s3PhotoSource struct {
	client *minio.Client
	bucket string
	prefix string
}

func newS3PhotoSource(cfg config) (*s3PhotoSource, error) {
	rawEndpoint := strings.TrimSpace(cfg.S3Endpoint)
	if !strings.Contains(rawEndpoint, "://") {
		scheme := "http"
		if cfg.S3UseSSL {
			scheme = "https"
		}
		rawEndpoint = scheme + "://" + rawEndpoint
	}
	u, err := url.Parse(rawEndpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("S3_ENDPOINT is invalid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("S3_ENDPOINT must use http or https")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("S3_ENDPOINT must not contain a path")
	}
	prefix, err := cleanObjectPrefix(cfg.S3Prefix)
	if err != nil {
		return nil, fmt.Errorf("S3_PREFIX: %w", err)
	}
	secure := u.Scheme == "https"
	transport, err := minio.DefaultTransport(secure)
	if err != nil {
		return nil, fmt.Errorf("create S3 transport: %w", err)
	}
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.TLSHandshakeTimeout = 5 * time.Second
	client, err := minio.New(u.Host, &minio.Options{
		Creds:      credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure:     secure,
		Region:     cfg.S3Region,
		Transport:  transport,
		MaxRetries: 2,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	return &s3PhotoSource{client: client, bucket: cfg.S3Bucket, prefix: prefix}, nil
}

func (s *s3PhotoSource) Name() string { return "s3" }

func (s *s3PhotoSource) Walk(ctx context.Context, visit func(sourceObject) error) error {
	objects := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    s.prefix,
		Recursive: true,
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
		version = "s3:" + version
		if err := visit(sourceObject{
			Key:     rel,
			RelPath: rel,
			Size:    object.Size,
			Mtime:   object.LastModified,
			Version: version,
			Backend: "s3",
		}); err != nil {
			return err
		}
	}
	return ctx.Err()
}

const s3SourceKeyPrefix = ".bijin-source/s3/"

type combinedPhotoSource struct {
	local photoSource
	s3    photoSource
}

func (s *combinedPhotoSource) Name() string { return "local+s3" }

func (s *combinedPhotoSource) Walk(ctx context.Context, visit func(sourceObject) error) error {
	if err := s.local.Walk(ctx, visit); err != nil {
		return err
	}
	return s.s3.Walk(ctx, func(object sourceObject) error {
		object.Key = s3SourceKeyPrefix + object.RelPath
		return visit(object)
	})
}

func (s *combinedPhotoSource) Open(ctx context.Context, key string) (readSeekCloser, error) {
	if strings.HasPrefix(key, s3SourceKeyPrefix) {
		return s.s3.Open(ctx, strings.TrimPrefix(key, s3SourceKeyPrefix))
	}
	return s.local.Open(ctx, key)
}

func (s *s3PhotoSource) Open(ctx context.Context, rel string) (readSeekCloser, error) {
	key, err := s.objectKey(rel)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
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
