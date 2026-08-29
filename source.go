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
	RelPath string
	Size    int64
	Mtime   time.Time
	Version string
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
	switch cfg.StorageBackend {
	case "local":
		return &localPhotoSource{root: cfg.PhotosDir}, nil
	case "s3":
		return newS3PhotoSource(cfg)
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
			RelPath: rel,
			Size:    info.Size(),
			Mtime:   info.ModTime(),
			Version: fmt.Sprintf("local:%d:%d", info.Size(), info.ModTime().UnixNano()),
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
			RelPath: rel,
			Size:    object.Size,
			Mtime:   object.LastModified,
			Version: version,
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
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, err
	}
	return object, nil
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
