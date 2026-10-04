package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCleanObjectPrefixAndObjectKey(t *testing.T) {
	prefix, err := cleanObjectPrefix("/albums/2026/")
	if err != nil || prefix != "albums/2026/" {
		t.Fatalf("prefix %q: %v", prefix, err)
	}
	source := &s3PhotoSource{prefix: prefix}
	key, err := source.objectKey("旅行/a.jpg")
	if err != nil || key != "albums/2026/旅行/a.jpg" {
		t.Fatalf("key %q: %v", key, err)
	}
	if _, err := source.objectKey("../secret.jpg"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if _, err := cleanObjectPrefix("ok/../bad"); err == nil {
		t.Fatal("expected bad prefix rejection")
	}
}

func TestKeyOwner(t *testing.T) {
	for key, want := range map[string]string{
		"a.jpg":                          localSourceID,
		"旅行/a.jpg":                       localSourceID,
		storageKeyPrefix(3) + "a.jpg":    "s3-3",
		storageKeyPrefix(12) + "x/y.jpg": "s3-12",
		".bijin-source/s3/old.jpg":       "s3",
	} {
		if got := keyOwner(key); got != want {
			t.Fatalf("keyOwner(%q) = %q, want %q", key, got, want)
		}
	}
}

type unavailableSource struct{}

func (unavailableSource) Walk(context.Context, func(sourceObject) error) error {
	return errors.New("source offline")
}

func (unavailableSource) Open(context.Context, string) (readSeekCloser, error) {
	return nil, errors.New("source offline")
}

func TestScanFailureKeepsExistingIndex(t *testing.T) {
	data := t.TempDir()
	st, err := openStore(filepath.Join(data, "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.upsert(photo{
		RelPath:       "existing.jpg",
		Size:          10,
		MtimeUnix:     time.Now().Unix(),
		Width:         100,
		Height:        100,
		SourceVersion: "v1",
	}); err != nil {
		t.Fatal(err)
	}
	source := unavailableSource{}
	thumbs := newThumbCache(filepath.Join(data, "thumbs"), source, 1)
	scanner := newScanner(config{MaxPixels: 64_000_000}, st, thumbs, newSourceSet(source))
	if err := scanner.walk(context.Background()); err == nil {
		t.Fatal("expected scan failure")
	}
	count, err := st.countOK()
	if err != nil || count != 1 {
		t.Fatalf("existing index lost: count=%d err=%v", count, err)
	}
}

type migrationLocalSource struct{ unavailableSource }

func TestLocalMigrationReusesExistingMetadataAndThumb(t *testing.T) {
	data := t.TempDir()
	st, err := openStore(filepath.Join(data, "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mtime := time.Now().Unix()
	id, err := st.upsert(photo{
		RelPath:   "existing.jpg",
		Size:      10,
		MtimeUnix: mtime,
		Width:     100,
		Height:    80,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := migrationLocalSource{}
	thumbs := newThumbCache(filepath.Join(data, "thumbs"), source, 1)
	if err := os.MkdirAll(thumbs.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(thumbs.dir, fmt.Sprintf("%d.jpg", id)), []byte("existing-thumb"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanner := newScanner(config{MaxPixels: 64_000_000}, st, thumbs, newSourceSet(source))
	object := sourceObject{
		Key:     "existing.jpg",
		RelPath: "existing.jpg",
		Size:    10,
		Mtime:   time.Unix(mtime, 0),
		Version: "local:10:123",
		Backend: "local",
	}
	if err := scanner.ingest(context.Background(), object); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.getBySourceKey("existing.jpg")
	if err != nil || !ok || got.SourceVersion != object.Version {
		t.Fatalf("version not backfilled: %+v ok=%v err=%v", got, ok, err)
	}
	thumb, err := os.ReadFile(thumbs.path(got))
	if err != nil || string(thumb) != "existing-thumb" {
		t.Fatalf("existing thumb changed: %q err=%v", thumb, err)
	}
}

// memSource is an in-memory photo source whose listing can be made to fail.
type memSource struct {
	mu    sync.Mutex
	files map[string][]byte
	fail  bool
	opens int
}

func newMemSource(files map[string][]byte) *memSource {
	return &memSource{files: files}
}

func (m *memSource) set(rel string, data []byte) {
	m.mu.Lock()
	m.files[rel] = data
	m.mu.Unlock()
}

func (m *memSource) remove(rel string) {
	m.mu.Lock()
	delete(m.files, rel)
	m.mu.Unlock()
}

func (m *memSource) setFail(fail bool) {
	m.mu.Lock()
	m.fail = fail
	m.mu.Unlock()
}

func (m *memSource) Walk(_ context.Context, visit func(sourceObject) error) error {
	m.mu.Lock()
	if m.fail {
		m.mu.Unlock()
		return errors.New("listing failed")
	}
	objects := make([]sourceObject, 0, len(m.files))
	for rel, data := range m.files {
		objects = append(objects, sourceObject{Key: rel, RelPath: rel, Size: int64(len(data)), Mtime: time.Unix(1700000000, 0), Version: fmt.Sprintf("mem:%d:%x", len(data), data[len(data)-8:]), Backend: "s3"})
	}
	m.mu.Unlock()
	for _, o := range objects {
		if err := visit(o); err != nil {
			return err
		}
	}
	return nil
}

func (m *memSource) Open(_ context.Context, rel string) (readSeekCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.opens++
	data, ok := m.files[rel]
	if !ok {
		return nil, os.ErrNotExist
	}
	return fixReader{bytes.NewReader(data)}, nil
}

func remote(id int64, src photoSource) namedSource {
	return namedSource{ID: storageSourceID(id), Name: fmt.Sprintf("remote %d", id), Prefix: storageKeyPrefix(id), Src: src}
}

func multiSetup(t *testing.T) (*store, *sourceSet, *scanner, *memSource, *memSource, *memSource) {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	local := newMemSource(map[string][]byte{"旅行/a.jpg": fixtureJPEG(color.RGBA{10, 20, 30, 255})})
	r1 := newMemSource(map[string][]byte{"旅行/a.jpg": fixtureJPEG(color.RGBA{90, 20, 30, 255}), "r1.jpg": fixtureJPEG(color.RGBA{1, 2, 3, 255})})
	r2 := newMemSource(map[string][]byte{"旅行/a.jpg": fixtureJPEG(color.RGBA{10, 90, 30, 255}), "r2.jpg": fixtureJPEG(color.RGBA{3, 2, 1, 255})})
	set := newSourceSet(local)
	set.setRemotes([]namedSource{remote(1, r1), remote(2, r2)})
	th := newThumbCache(filepath.Join(t.TempDir(), "thumbs"), set, 1)
	return st, set, newScanner(config{MaxPixels: 64_000_000}, st, th, set), local, r1, r2
}

func keysOf(t *testing.T, st *store) map[string]photo {
	t.Helper()
	all, err := st.listOK()
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]photo, len(all))
	for _, p := range all {
		out[p.SourceKey] = p
	}
	return out
}

func TestMultipleSourcesKeepSameDisplayPathSeparately(t *testing.T) {
	st, set, sc, local, r1, r2 := multiSetup(t)
	if err := sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	keys := keysOf(t, st)
	want := []string{"旅行/a.jpg", storageKeyPrefix(1) + "旅行/a.jpg", storageKeyPrefix(1) + "r1.jpg", storageKeyPrefix(2) + "旅行/a.jpg", storageKeyPrefix(2) + "r2.jpg"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v", keys)
	}
	for _, k := range want {
		if keys[k].RelPath != strings.TrimPrefix(strings.TrimPrefix(k, storageKeyPrefix(1)), storageKeyPrefix(2)) {
			t.Fatalf("missing or wrong display path for %q: %+v", k, keys[k])
		}
	}
	albums := groupAlbums(mapValues(keys))
	if len(albums) != 2 || albums[0].Count+albums[1].Count != 5 {
		t.Fatalf("albums = %+v", albums)
	}
	// Each key must be read from its own source.
	for key, src := range map[string]*memSource{"旅行/a.jpg": local, storageKeyPrefix(1) + "旅行/a.jpg": r1, storageKeyPrefix(2) + "旅行/a.jpg": r2} {
		f, err := set.Open(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(f)
		f.Close()
		if !bytes.Equal(got, src.files["旅行/a.jpg"]) {
			t.Fatalf("%s read from the wrong source", key)
		}
	}
	if _, err := set.Open(context.Background(), storageKeyPrefix(9)+"x.jpg"); err == nil {
		t.Fatal("unknown storage opened")
	}
	status := sc.sourceStatuses()
	if status["local"].Seen != 1 || status["s3-1"].Seen != 2 || status["s3-2"].Seen != 2 || status["s3-1"].Err != "" {
		t.Fatalf("status = %+v", status)
	}
}

func mapValues(m map[string]photo) []photo {
	out := make([]photo, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	return out
}

func TestOneFailingStorageDoesNotBlockOthers(t *testing.T) {
	st, _, sc, local, r1, r2 := multiSetup(t)
	if err := sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := keysOf(t, st)
	r1.setFail(true)
	r1.remove("r1.jpg") // must stay indexed: the listing failed
	r2.remove("r2.jpg") // must be removed: this storage listed fine
	r2.set("new.jpg", fixtureJPEG(color.RGBA{50, 50, 50, 255}))
	local.set("new-local.jpg", fixtureJPEG(color.RGBA{60, 60, 60, 255}))
	err := sc.walk(context.Background())
	if err == nil || !strings.Contains(err.Error(), "s3-1") {
		t.Fatalf("walk error = %v", err)
	}
	keys := keysOf(t, st)
	if _, ok := keys[storageKeyPrefix(1)+"r1.jpg"]; !ok {
		t.Fatal("failed storage lost its photo")
	}
	if keys[storageKeyPrefix(1)+"r1.jpg"].ID != before[storageKeyPrefix(1)+"r1.jpg"].ID {
		t.Fatal("failed storage photo was re-created")
	}
	if _, ok := keys[storageKeyPrefix(2)+"r2.jpg"]; ok {
		t.Fatal("deleted object of healthy storage kept")
	}
	if _, ok := keys[storageKeyPrefix(2)+"new.jpg"]; !ok {
		t.Fatal("healthy storage after a failing one was not scanned")
	}
	if _, ok := keys["new-local.jpg"]; !ok {
		t.Fatal("local source not scanned")
	}
	status := sc.sourceStatuses()
	if status["s3-1"].Err == "" || status["s3-2"].Err != "" || status["local"].Err != "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestRemovedStorageDropsItsPhotosAndThumbs(t *testing.T) {
	st, set, sc, _, r1, _ := multiSetup(t)
	if err := sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	gone := keysOf(t, st)[storageKeyPrefix(2)+"r2.jpg"]
	if !sc.thumbs.exists(gone) {
		t.Fatal("thumbnail missing before removal")
	}
	set.setRemotes([]namedSource{remote(1, r1)})
	if err := sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	for key := range keysOf(t, st) {
		if keyOwner(key) == "s3-2" {
			t.Fatalf("removed storage row kept: %s", key)
		}
	}
	if sc.thumbs.exists(gone) {
		t.Fatal("removed storage thumbnail kept")
	}
	if n, _ := st.countOK(); n != 3 {
		t.Fatalf("remaining photos = %d", n)
	}
}

func TestLegacySingleS3RowsAreCleanedUp(t *testing.T) {
	st, _, sc, _, _, _ := multiSetup(t)
	if _, err := st.upsert(photo{SourceKey: ".bijin-source/s3/old.jpg", RelPath: "old.jpg", Width: 10, Height: 10, SourceVersion: "s3:x"}); err != nil {
		t.Fatal(err)
	}
	if err := sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := keysOf(t, st)[".bijin-source/s3/old.jpg"]; ok {
		t.Fatal("legacy row kept")
	}
}

func TestCancelledScanDeletesNothing(t *testing.T) {
	st, _, sc, local, _, _ := multiSetup(t)
	if err := sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	local.remove("旅行/a.jpg")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sc.walk(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("walk = %v", err)
	}
	if _, ok := keysOf(t, st)["旅行/a.jpg"]; !ok {
		t.Fatal("cancelled scan deleted a photo")
	}
}

func TestTriggerStartsScanWithoutWaitingForTicker(t *testing.T) {
	st, _, sc, local, _, _ := multiSetup(t)
	sc.cfg.ScanEvery = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sc.loop(ctx)
	waitFor(t, func() bool { n, _ := st.countOK(); return n == 5 && !sc.snapshot().Scanning })
	local.set("added.jpg", fixtureJPEG(color.RGBA{7, 7, 7, 255}))
	sc.trigger()
	if !sc.snapshot().Queued && !sc.snapshot().Scanning {
		if n, _ := st.countOK(); n != 6 {
			t.Fatal("trigger not visible as queued or scanning")
		}
	}
	waitFor(t, func() bool { n, _ := st.countOK(); return n == 6 })
	waitFor(t, func() bool { s := sc.snapshot(); return !s.Queued && !s.Scanning })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStoreKeepsRowsWithSameDisplayPath(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, p := range []photo{
		{SourceKey: "旅行/a.jpg", RelPath: "旅行/a.jpg", Width: 100, Height: 80, SourceVersion: "local:v1"},
		{SourceKey: storageKeyPrefix(1) + "旅行/a.jpg", RelPath: "旅行/a.jpg", Width: 100, Height: 80, SourceVersion: "s3:v1"},
		{SourceKey: storageKeyPrefix(2) + "旅行/a.jpg", RelPath: "旅行/a.jpg", Width: 100, Height: 80, SourceVersion: "s3:v1"},
		{SourceKey: "bad.jpg", RelPath: "bad.jpg", Broken: true, SourceVersion: "local:v1"},
		{SourceKey: "pending.jpg", RelPath: "pending.jpg", Broken: true},
	} {
		if _, err := st.upsert(p); err != nil {
			t.Fatal(err)
		}
	}
	photos, err := st.listOK()
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 3 {
		t.Fatalf("unexpected photos: %+v", photos)
	}
	counts, err := st.countByOwner()
	if err != nil || counts["local"] != (ownerCount{OK: 1, Broken: 1}) || counts["s3-1"].OK != 1 || counts["s3-2"].OK != 1 {
		t.Fatalf("counts = %v err=%v", counts, err)
	}
}

func TestS3SourceListsV2ByDefaultAndV1WhenAsked(t *testing.T) {
	for _, v1 := range []bool{false, true} {
		t.Run(fmt.Sprint("v1=", v1), func(t *testing.T) {
			f := newFakeS3(t, "photos")
			f.put("gallery/a.jpg", fixtureJPEG(color.RGBA{1, 1, 1, 255}))
			f.put("gallery/sub/b.png", []byte("png"))
			f.put("gallery/notes.txt", []byte("x"))
			f.put("gallery/.hidden/c.jpg", []byte("x"))
			f.put("other/d.jpg", []byte("x"))
			c := f.config()
			c.Prefix = "gallery"
			c.ListV1 = v1
			src, err := newS3PhotoSource(c)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			if err := src.Walk(context.Background(), func(o sourceObject) error {
				got = append(got, o.RelPath)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != "a.jpg,sub/b.png" {
				t.Fatalf("objects = %v", got)
			}
			usedV2 := false
			for _, r := range f.log() {
				if strings.HasPrefix(r, "GET /photos/?") && strings.Contains(r, "list-type=2") {
					usedV2 = true
				}
			}
			if usedV2 == v1 {
				t.Fatalf("v1=%v but requests were %v", v1, f.log())
			}
			rc, err := src.Open(context.Background(), "a.jpg")
			if err != nil {
				t.Fatal(err)
			}
			rc.Close()
		})
	}
}

func TestS3RegionDetection(t *testing.T) {
	cases := []struct {
		location, configured, want string
	}{
		{"eu-test-1", "", "eu-test-1"},          // detected from GetBucketLocation
		{"", "", "us-east-1"},                   // NotImplemented falls back
		{"eu-test-1", "ap-test-2", "ap-test-2"}, // configured region wins, no lookup
	}
	for _, tc := range cases {
		f := newFakeS3(t, "photos")
		f.location = tc.location
		f.put("a.jpg", []byte("x"))
		c := f.config()
		c.Region = tc.configured
		src, err := newS3PhotoSource(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := src.Walk(context.Background(), func(sourceObject) error { return nil }); err != nil {
			t.Fatal(err)
		}
		auths := f.authLog()
		last := auths[len(auths)-1]
		if !strings.Contains(last, "/"+tc.want+"/s3/aws4_request") {
			t.Fatalf("%+v: signed with %q", tc, last)
		}
		lookups := 0
		for _, r := range f.log() {
			if strings.Contains(r, "location=") {
				lookups++
			}
		}
		if (tc.configured == "") != (lookups == 1) {
			t.Fatalf("%+v: location lookups = %d", tc, lookups)
		}
	}
}

func TestS3NetworkErrorIsNotCachedAsRegion(t *testing.T) {
	f := newFakeS3(t, "photos")
	f.location = "eu-test-1"
	f.put("a.jpg", []byte("x"))
	src, err := newS3PhotoSource(f.config())
	if err != nil {
		t.Fatal(err)
	}
	f.setDown(true)
	if err := src.Walk(context.Background(), func(sourceObject) error { return nil }); err == nil {
		t.Fatal("expected failure while service is down")
	}
	f.setDown(false)
	if err := src.Walk(context.Background(), func(sourceObject) error { return nil }); err != nil {
		t.Fatal(err)
	}
	auths := f.authLog()
	if !strings.Contains(auths[len(auths)-1], "/eu-test-1/") {
		t.Fatalf("region after recovery: %s", auths[len(auths)-1])
	}
}

func TestProbeAndErrorHints(t *testing.T) {
	f := newFakeS3(t, "photos")
	for i := 0; i < 5; i++ {
		f.put(fmt.Sprintf("p/%d.jpg", i), []byte("x"))
	}
	f.put("p/readme.txt", []byte("x"))
	c := f.config()
	c.Prefix = "p"
	src, _ := newS3PhotoSource(c)
	res, err := src.probe(context.Background(), 1000)
	if err != nil || res.Images != 5 || res.Checked != 6 || res.More {
		t.Fatalf("probe = %+v err=%v", res, err)
	}
	res, err = src.probe(context.Background(), 3)
	if err != nil || res.Checked != 3 || !res.More {
		t.Fatalf("limited probe = %+v err=%v", res, err)
	}

	bad := f.config()
	bad.AccessKey = "wrong"
	src, _ = newS3PhotoSource(bad)
	_, err = src.probe(context.Background(), 10)
	if msg := describeSourceError(err); !strings.Contains(msg, "Access Key 不存在") {
		t.Fatalf("hint = %q", msg)
	}
	missing := f.config()
	missing.Bucket = "nope"
	src, _ = newS3PhotoSource(missing)
	_, err = src.probe(context.Background(), 10)
	if msg := describeSourceError(err); !strings.Contains(msg, "Bucket 不存在") {
		t.Fatalf("hint = %q", msg)
	}
	tlsToHTTP := f.config()
	tlsToHTTP.Endpoint = strings.Replace(f.srv.URL, "http://", "https://", 1)
	src, _ = newS3PhotoSource(tlsToHTTP)
	_, err = src.probe(context.Background(), 10)
	if msg := describeSourceError(err); !strings.Contains(msg, "请在地址前写 http://") {
		t.Fatalf("hint = %q", msg)
	}
	closed := f.config()
	closed.Endpoint = "http://127.0.0.1:1"
	src, _ = newS3PhotoSource(closed)
	_, err = src.probe(context.Background(), 10)
	if msg := describeSourceError(err); !strings.Contains(msg, "连不上 Endpoint") {
		t.Fatalf("hint = %q", msg)
	}
}

func TestBrokenStorageConfigKeepsItsPhotos(t *testing.T) {
	st, set, sc, _, _, _ := multiSetup(t)
	if err := sc.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A stored config the client cannot use must not look like a removed storage.
	set.useStorages([]s3Config{{ID: 1, Name: "bad", Endpoint: "ftp://x", Bucket: "b", AccessKey: "a", SecretKey: "s"}})
	if err := sc.walk(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	keys := keysOf(t, st)
	if _, ok := keys[storageKeyPrefix(1)+"r1.jpg"]; !ok {
		t.Fatal("photos of a misconfigured storage were removed")
	}
	if _, ok := keys[storageKeyPrefix(2)+"r2.jpg"]; ok {
		t.Fatal("storage missing from config kept its photos")
	}
}
