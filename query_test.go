package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func queryStore(t *testing.T) *store {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "query.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func queryPhoto(t *testing.T, st *store, path string, at int64) photo {
	t.Helper()
	p := photo{RelPath: path, Size: 5 << 20, MtimeUnix: at, Width: 600, Height: 400, SourceVersion: "v1"}
	id, err := st.upsert(p)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.SourceKey = id, path
	return p
}

func TestQueryPagesPreserveLegacyOrder(t *testing.T) {
	st := queryStore(t)
	for i := 0; i < 207; i++ {
		queryPhoto(t, st, fmt.Sprintf("旅行/%03d.jpg", i), int64(i))
	}
	for _, seed := range []int64{42, -17, 9223372036854775807} {
		wanted, err := st.listOK()
		if err != nil {
			t.Fatal(err)
		}
		sortByRank(wanted, seed)
		var got []photo
		var rank uint64
		var id int64
		for page := 0; page < 30; page++ {
			photos, total, next, err := st.photoPage(seed, "", rank, id, 19)
			if err != nil || total != len(wanted) {
				t.Fatalf("page: total=%d err=%v", total, err)
			}
			got = append(got, photos...)
			if next == nil {
				break
			}
			rank, id = parseCursor(*next)
		}
		if !reflect.DeepEqual(got, wanted) {
			t.Fatal("cached pages differ from legacy order or omit/duplicate photos")
		}
	}
	builds := st.cache.orders.builds.Load()
	if _, _, _, err := st.photoPage(42, "", 0, 0, 40); err != nil {
		t.Fatal(err)
	}
	if st.cache.orders.builds.Load() != builds || st.cache.index.builds.Load() != 1 {
		t.Fatal("unchanged pages rebuilt the full index or random order")
	}
}

func TestQueryVisibleScopeDetailsAndInvalidation(t *testing.T) {
	st := queryStore(t)
	a := queryPhoto(t, st, "same/a.jpg", 10)
	b := photo{SourceKey: storageKeyPrefix(1) + "same/a.jpg", RelPath: "same/a.jpg", Width: 600, Height: 400, Size: 20}
	bID, err := st.upsert(b)
	if err != nil {
		t.Fatal(err)
	}
	b.ID = bID
	other := queryPhoto(t, st, "other/c.jpg", 20)
	photos, total, _, err := st.photoPage(7, "same", 0, 0, 40)
	if err != nil || total != 2 || len(photos) != 2 {
		t.Fatalf("same-folder sources: %d %v", total, err)
	}
	indexBuilds, orderBuilds := st.cache.index.builds.Load(), st.cache.orders.builds.Load()
	a.Size, a.SourceVersion = 12345, "v2"
	if _, err := st.upsert(a); err != nil {
		t.Fatal(err)
	}
	photos, _, _, err = st.photoPage(7, "same", 0, 0, 40)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range photos {
		if p.ID == a.ID && (p.Size != 12345 || p.SourceVersion != "v2") {
			t.Fatal("page served cached details")
		}
	}
	if st.cache.index.builds.Load() != indexBuilds || st.cache.orders.builds.Load() != orderBuilds {
		t.Fatal("metadata-only update reshuffled the visible ID set")
	}
	a.RelPath = "other/a.jpg"
	if _, err := st.upsert(a); err != nil {
		t.Fatal(err)
	}
	photos, total, _, err = st.photoPage(7, "same", 0, 0, 40)
	if err != nil || total != 1 || len(photos) != 1 || photos[0].ID != b.ID {
		t.Fatalf("move: %+v total=%d err=%v", photos, total, err)
	}
	other.Broken = true
	if _, err := st.upsert(other); err != nil {
		t.Fatal(err)
	}
	if n, err := st.countOK(); err != nil || n != 2 {
		t.Fatalf("broken count %d %v", n, err)
	}
	if err := st.deleteByID(b.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := st.countOK(); err != nil || n != 1 {
		t.Fatalf("delete count %d %v", n, err)
	}
}

func TestQuerySourceCountsPendingBrokenAndRecovered(t *testing.T) {
	st := queryStore(t)
	queryPhoto(t, st, "ready.jpg", 1)
	first, err := st.countByOwner()
	if err != nil || first["local"].OK != 1 {
		t.Fatal(first, err)
	}
	if _, err := st.countByOwner(); err != nil {
		t.Fatal(err)
	}
	if st.cache.owners.builds.Load() != 1 {
		t.Fatal("unchanged source counts rebuilt")
	}
	p := photo{RelPath: "pending.jpg", Broken: true}
	if _, err := st.upsert(p); err != nil {
		t.Fatal(err)
	}
	counts, err := st.countByOwner()
	if err != nil || counts["local"].Broken != 0 {
		t.Fatal("pending counted as broken", counts, err)
	}
	p.SourceVersion = "bad-v1"
	if _, err := st.upsert(p); err != nil {
		t.Fatal(err)
	}
	counts, err = st.countByOwner()
	if err != nil || counts["local"].Broken != 1 {
		t.Fatal("broken count stale", counts, err)
	}
	p.Broken, p.Width, p.Height = false, 10, 10
	if _, err := st.upsert(p); err != nil {
		t.Fatal(err)
	}
	counts, err = st.countByOwner()
	if err != nil || counts["local"].OK != 2 || counts["local"].Broken != 0 {
		t.Fatal("recovery count stale", counts, err)
	}
}

func TestQueryPageSkipsStaleCandidatesAndFillsPage(t *testing.T) {
	st := queryStore(t)
	for i := 0; i < 25; i++ {
		queryPhoto(t, st, fmt.Sprintf("a/%d.jpg", i), int64(i))
	}
	index, _ := st.photoIndex()
	order, _ := st.photoOrder(index, 42, "a")
	// Simulate a commit racing the cache invalidation callback.
	if _, err := st.db.Exec("UPDATE photos SET broken=1 WHERE id IN (?,?,?)", order[0].id, order[3].id, order[7].id); err != nil {
		t.Fatal(err)
	}
	photos, _, next, err := st.photoPage(42, "a", 0, 0, 10)
	if err != nil || len(photos) != 10 || next == nil {
		t.Fatalf("fill: %d %v %v", len(photos), next, err)
	}
	for _, p := range photos {
		if p.ID == order[0].id || p.ID == order[3].id || p.ID == order[7].id {
			t.Fatal("invalid cached photo returned")
		}
	}
	rank, id := parseCursor(*next)
	if id != order[12].id || rank != order[12].rank {
		t.Fatal("cursor did not advance past consumed candidates")
	}
}

func TestQueryAlbumSummaryAndPeopleInvalidation(t *testing.T) {
	st := queryStore(t)
	old := queryPhoto(t, st, "a/old.jpg", 10)
	newer := queryPhoto(t, st, "a/new.jpg", 20)
	tie := queryPhoto(t, st, "a/tie.jpg", 20)
	queryPhoto(t, st, "root.jpg", 30)
	queryPhoto(t, st, "a/deep/photo.jpg", 40)
	all, _ := st.listOK()
	want := groupAlbums(all)
	got, err := st.albumSummaries()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("summary differs: %+v / %+v err=%v", got, want, err)
	}
	if _, err := st.albumSummaries(); err != nil {
		t.Fatal(err)
	}
	if st.cache.albums.builds.Load() != 1 {
		t.Fatal("unchanged albums rebuilt")
	}
	author := "摄影师"
	if err := st.setAlbumPeople([]string{"a"}, &author, []string{"模特"}); err != nil {
		t.Fatal(err)
	}
	links, err := st.albumPeopleMap()
	if err != nil || links["a"].Author == nil || links["a"].Author.Name != author {
		t.Fatal("assignment not visible")
	}
	id := links["a"].Author.ID
	if _, _, err := st.renamePerson(id, "新名字"); err != nil {
		t.Fatal(err)
	}
	links, _ = st.albumPeopleMap()
	if links["a"].Author.Name != "新名字" {
		t.Fatal("name cache not invalidated")
	}
	if st.cache.albums.builds.Load() != 1 {
		t.Fatal("people change rebuilt photo summaries")
	}
	if err := st.deleteByID(tie.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.albumSummaries()
	for _, a := range got {
		if a.ID == "a" && (a.Cover.ID != newer.ID || a.Count != 2 || a.Added != old.MtimeUnix) {
			t.Fatal("cover/count did not update")
		}
	}
	if err := st.deletePerson(id); err != nil {
		t.Fatal(err)
	}
	links, _ = st.albumPeopleMap()
	if links["a"].Author != nil {
		t.Fatal("deleted author remained cached")
	}
}

func TestQueryMigrationBackfillsDirectoriesAndRollbackWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE photos(
 id INTEGER PRIMARY KEY,rel_path TEXT NOT NULL UNIQUE,size INTEGER NOT NULL,mtime_unix INTEGER NOT NULL,
 width INTEGER NOT NULL DEFAULT 0,height INTEGER NOT NULL DEFAULT 0,broken INTEGER NOT NULL DEFAULT 0,
 source_version TEXT NOT NULL DEFAULT '',display_path TEXT NOT NULL DEFAULT '');
 INSERT INTO photos VALUES(1,'.bijin-source/s3-1/a.jpg',1,1,10,10,0,'v1','2026/旅行/a.jpg');
 INSERT INTO photos VALUES(2,'root.jpg',1,1,10,10,0,'v1','root.jpg');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var album string
	if err := st.db.QueryRow("SELECT album_path FROM photos WHERE id=1").Scan(&album); err != nil || album != "2026/旅行" {
		t.Fatalf("backfill %q %v", album, err)
	}
	if err := st.db.QueryRow("SELECT album_path FROM photos WHERE id=2").Scan(&album); err != nil || album != "." {
		t.Fatalf("root %q %v", album, err)
	}
	// An older binary omits the new column when inserting.
	_, err = st.db.Exec("INSERT INTO photos(rel_path,display_path,size,mtime_unix,width,height) VALUES('after/a.jpg','after/a.jpg',1,1,10,10)")
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.db.QueryRow("SELECT album_path FROM photos WHERE rel_path='after/a.jpg'").Scan(&album); err != nil || album != "after" {
		t.Fatalf("rollback repair %q %v", album, err)
	}
}

func TestQueryPlansUseIndexes(t *testing.T) {
	st := queryStore(t)
	queryPhoto(t, st, "a/a.jpg", 1)
	for _, tc := range []struct {
		query, index string
		args         []any
	}{
		{"SELECT id FROM photos WHERE " + visiblePhotos, "photos_visible_id", nil},
		{"SELECT id FROM photos WHERE album_path=? AND " + visiblePhotos + " ORDER BY mtime_unix DESC,id DESC LIMIT 1", "photos_visible_album_time", []any{"a"}},
		{"SELECT " + photoColumns + " FROM photos WHERE id IN (?,?) AND " + visiblePhotos, "INTEGER PRIMARY KEY", []any{1, 2}},
	} {
		rows, err := st.db.Query("EXPLAIN QUERY PLAN "+tc.query, tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		var details []string
		for rows.Next() {
			var a, b, c int
			var d string
			if err := rows.Scan(&a, &b, &c, &d); err != nil {
				t.Fatal(err)
			}
			details = append(details, d)
		}
		rows.Close()
		if !strings.Contains(strings.Join(details, " "), tc.index) {
			t.Fatalf("missing index %q: %v", tc.index, details)
		}
	}
}

func TestQueryWarmCountDoesNotWaitForDatabase(t *testing.T) {
	st := queryStore(t)
	queryPhoto(t, st, "a.jpg", 1)
	if n, err := st.countOK(); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	conn, err := st.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan int, 1)
	go func() {
		n, err := st.countOK()
		if err != nil {
			n = -1
		}
		done <- n
	}()
	select {
	case n := <-done:
		if n != 1 {
			t.Fatal(n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("warm count performed another database query")
	}
}

func TestQueryCachesSingleBuildStalePublishAndLimits(t *testing.T) {
	var revision atomic.Uint64
	var cached revisionCache[int]
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan int, 1)
	go func() {
		v, _ := cached.get(&revision, func(uint64) (int, error) { close(started); <-release; return 1, nil })
		done <- v
	}()
	<-started
	revision.Add(1)
	close(release)
	if <-done != 1 {
		t.Fatal("request lost its consistent snapshot")
	}
	v, err := cached.get(&revision, func(uint64) (int, error) { return 2, nil })
	if err != nil || v != 2 {
		t.Fatal("stale snapshot published as current")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, _ := cached.get(&revision, func(uint64) (int, error) { return 3, nil })
			if v != 2 {
				t.Error("cache hit changed")
			}
		}()
	}
	wg.Wait()
	if cached.builds.Load() != 2 {
		t.Fatal("same revision rebuilt")
	}
	var bounded boundedCache[int, []int64]
	limits := cacheLimits{2, 160, time.Hour}
	for i := 0; i < 5; i++ {
		if _, err := bounded.get(i, limits, func() ([]int64, error) { return make([]int64, 8), nil }, func([]int64) int64 { return 80 }); err != nil {
			t.Fatal(err)
		}
	}
	bounded.mu.Lock()
	if len(bounded.entries) != 2 || bounded.bytes > 160 {
		t.Fatal("cache limits exceeded")
	}
	for key, entry := range bounded.entries {
		entry.used = time.Now().Add(-2 * time.Hour)
		bounded.entries[key] = entry
	}
	bounded.mu.Unlock()
	if _, ok := bounded.lookup(4, limits); ok {
		t.Fatal("expired cache retained")
	}
}

func TestQueryWallpaperFairnessFiltersAndStaleValidation(t *testing.T) {
	st := queryStore(t)
	for i := 1; i <= 3; i++ {
		p := queryPhoto(t, st, fmt.Sprintf("%d.jpg", i), int64(i))
		var variants []wallpaper
		profiles := []string{"desktop-3840"}
		orientation := "landscape"
		if i == 2 {
			profiles = []string{"mobile-1440"}
			orientation = "portrait"
		}
		if i == 3 {
			profiles = []string{"desktop-3840", "mobile-1440"}
			orientation = "square"
		}
		for _, profile := range profiles {
			variants = append(variants, wallpaper{PhotoID: p.ID, Profile: profile, SourceVersion: p.SourceVersion, Orientation: orientation, Width: 600, Height: 400, Bytes: 100, SHA256: fmt.Sprintf("%064x", i), Color: "#123456", File: profile + ".webp"})
		}
		if err := st.replaceWallpapers(p.ID, variants); err != nil {
			t.Fatal(err)
		}
	}
	counts := make(map[int64]int)
	for i := 0; i < 9000; i++ {
		v, ok, err := st.randomWallpaper(wallFilter{})
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		counts[v.PhotoID]++
	}
	for id, n := range counts {
		if n < 2600 || n > 3400 {
			t.Fatalf("photo %d selected %d times; square must not have double weight", id, n)
		}
	}
	if len(counts) != 3 {
		t.Fatal("a photo was never selected")
	}
	v, ok, err := st.randomWallpaper(wallFilter{orientation: "square", profile: "mobile-1440", minW: 600, minH: 400})
	if err != nil || !ok || v.PhotoID != 3 || v.Profile != "mobile-1440" {
		t.Fatal(v, ok, err)
	}
	if _, ok, err := st.randomWallpaper(wallFilter{minW: 601}); err != nil || ok {
		t.Fatal("impossible size matched", ok, err)
	}
	// Cache a single-photo selection, then simulate a delete before its hook.
	if _, err := st.db.Exec("DELETE FROM wallpapers WHERE photo_id=3"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.randomWallpaper(wallFilter{orientation: "square", profile: "mobile-1440", minW: 600, minH: 400}); err != nil || ok {
		t.Fatal("stale candidate served", ok, err)
	}
}

func TestQueryConcurrentReadsAndWrites(t *testing.T) {
	st := queryStore(t)
	p := queryPhoto(t, st, "a/main.jpg", 1)
	var wg sync.WaitGroup
	for reader := 0; reader < 5; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if _, _, _, err := st.photoPage(42, "", 0, 0, 40); err != nil {
					t.Error(err)
					return
				}
				if _, err := st.albumSummaries(); err != nil {
					t.Error(err)
					return
				}
				if _, err := st.countByOwner(); err != nil {
					t.Error(err)
					return
				}
				if _, err := st.wallpaperStats(); err != nil {
					t.Error(err)
					return
				}
				if _, _, err := st.randomWallpaper(wallFilter{}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := 0; i < 40; i++ {
		p.Broken = i%2 == 0
		p.SourceVersion = fmt.Sprintf("v%d", i)
		if _, err := st.upsert(p); err != nil {
			t.Fatal(err)
		}
		if err := st.replaceWallpapers(p.ID, []wallpaper{{PhotoID: p.ID, Profile: "desktop-3840", SourceVersion: p.SourceVersion, Orientation: "landscape", Width: 600, Height: 400, Bytes: 10, SHA256: fmt.Sprintf("%064x", i), File: "fixture.webp", Color: "#000000"}}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	photos, n, _, err := st.photoPage(42, "", 0, 0, 40)
	if err != nil || n != 1 || len(photos) != 1 || photos[0].SourceVersion != "v39" {
		t.Fatal("final state not visible", n, err)
	}
	albums, err := st.albumSummaries()
	if err != nil || len(albums) != 1 || albums[0].Cover.SourceVersion != "v39" {
		t.Fatal("final cover stale", err)
	}
}

func TestQueryOrderEvictionRebuildsSameCursor(t *testing.T) {
	st := queryStore(t)
	for i := 0; i < 100; i++ {
		queryPhoto(t, st, fmt.Sprintf("%d.jpg", i), int64(i))
	}
	first, _, cursor, err := st.photoPage(42, "", 0, 0, 40)
	if err != nil || cursor == nil {
		t.Fatal(err)
	}
	rank, id := parseCursor(*cursor)
	want, _, _, _ := st.photoPage(42, "", rank, id, 40)
	for seed := int64(100); seed < 112; seed++ {
		if _, _, _, err := st.photoPage(seed, "", 0, 0, 40); err != nil {
			t.Fatal(err)
		}
	}
	got, _, _, err := st.photoPage(42, "", rank, id, 40)
	if err != nil || !slices.Equal(want, got) || len(first) != 40 {
		t.Fatal("eviction changed cursor order", err)
	}
	st.cache.orders.mu.Lock()
	defer st.cache.orders.mu.Unlock()
	if len(st.cache.orders.entries) > photoOrderLimits.entries || st.cache.orders.bytes > photoOrderLimits.bytes {
		t.Fatal("order cache unbounded")
	}
}
