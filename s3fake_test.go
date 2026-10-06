package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 is a small path-style S3 server: list (V1/V2), bucket location,
// HEAD/GET object and access-key checks. It records requests for assertions.
type fakeS3 struct {
	t         *testing.T
	srv       *httptest.Server
	bucket    string
	accessKey string
	location  string // empty: GetBucketLocation returns NotImplemented

	mu        sync.Mutex
	objects   map[string][]byte
	uploadIDs map[string]string
	down      bool
	delay     time.Duration // added to every object GET
	requests  []string
	auths     []string
}

func newFakeS3(t *testing.T, bucket string) *fakeS3 {
	f := &fakeS3{t: t, bucket: bucket, accessKey: "test-ak", location: "us-east-1", objects: map[string][]byte{}, uploadIDs: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeS3) put(key string, data []byte) {
	f.mu.Lock()
	f.objects[key] = data
	f.mu.Unlock()
}

func (f *fakeS3) del(key string) {
	f.mu.Lock()
	delete(f.objects, key)
	f.mu.Unlock()
}

func (f *fakeS3) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

func (f *fakeS3) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeS3) authLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...)
}

func (f *fakeS3) config() s3Config {
	return s3Config{ID: 1, Name: "fake", Endpoint: f.srv.URL, Bucket: f.bucket, AccessKey: f.accessKey, SecretKey: "test-sk", Addressing: addressingPath}
}

type fakeObject struct {
	Key          string
	LastModified string
	ETag         string
	Size         int
}

type fakeList struct {
	XMLName     xml.Name `xml:"ListBucketResult"`
	Name        string
	Prefix      string
	KeyCount    int `xml:",omitempty"`
	MaxKeys     int
	IsTruncated bool
	Contents    []fakeObject
}

func fakeError(w http.ResponseWriter, code int, s3code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(code)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, s3code, s3code)
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	down := f.down
	f.mu.Unlock()
	if down {
		fakeError(w, http.StatusServiceUnavailable, "ServiceUnavailable")
		return
	}
	// Header or presigned query credentials; signatures are not verified.
	if !strings.Contains(r.Header.Get("Authorization"), "Credential="+f.accessKey+"/") &&
		!strings.HasPrefix(r.URL.Query().Get("X-Amz-Credential"), f.accessKey+"/") {
		fakeError(w, http.StatusForbidden, "InvalidAccessKeyId")
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != f.bucket {
		fakeError(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	q := r.URL.Query()
	lastModified := time.Unix(1700000000, 0).UTC()
	switch {
	case key != "" && r.Method == http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			fakeError(w, 400, "IncompleteBody")
			return
		}
		f.mu.Lock()
		if _, exists := f.objects[key]; exists && r.Header.Get("If-None-Match") == "*" {
			f.mu.Unlock()
			fakeError(w, 412, "PreconditionFailed")
			return
		}
		f.objects[key] = body
		f.uploadIDs[key] = r.Header.Get("X-Amz-Meta-Bijin-Upload")
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case key == "" && q.Has("location"):
		if f.location == "" {
			fakeError(w, http.StatusNotImplemented, "NotImplemented")
			return
		}
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">%s</LocationConstraint>`, f.location)
	case key == "" && r.Method == http.MethodGet:
		prefix := q.Get("prefix")
		f.mu.Lock()
		keys := make([]string, 0, len(f.objects))
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		res := fakeList{Name: f.bucket, Prefix: prefix, MaxKeys: 1000}
		for _, k := range keys {
			res.Contents = append(res.Contents, fakeObject{Key: k, LastModified: lastModified.Format("2006-01-02T15:04:05.000Z"), ETag: fmt.Sprintf(`"%x"`, len(f.objects[k])*31+len(k)), Size: len(f.objects[k])})
		}
		f.mu.Unlock()
		if q.Get("list-type") == "2" {
			res.KeyCount = len(res.Contents)
		}
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(res)
	default:
		f.mu.Lock()
		data, ok := f.objects[key]
		delay := f.delay
		uploadID := f.uploadIDs[key]
		f.mu.Unlock()
		if !ok {
			fakeError(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		if r.Method == http.MethodGet {
			time.Sleep(delay)
		}
		w.Header().Set("ETag", fmt.Sprintf(`"%x"`, len(data)*31+len(key)))
		w.Header().Set("Last-Modified", lastModified.Format(http.TimeFormat))
		w.Header().Set("Content-Type", "image/jpeg")
		if uploadID != "" {
			w.Header().Set("X-Amz-Meta-Bijin-Upload", uploadID)
		}
		http.ServeContent(w, r, key, lastModified, strings.NewReader(string(data)))
	}
}
