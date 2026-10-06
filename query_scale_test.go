package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in: representative metadata and real HTTP handlers, never production
// data or a production object store. It does not simulate image transfer.
func TestQueryScale(t *testing.T) {
	if os.Getenv("BIJIN_QUERY_SCALE") != "1" {
		t.Skip("run with BIJIN_QUERY_SCALE=1 for the 100k/200k isolated HTTP load test")
	}
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(oldLogger)
	for _, count := range []int{100000, 200000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			st := queryStore(t)
			start := time.Now()
			populateQueryScale(t, st, count)
			t.Logf("metadata=%d populate=%s", count, time.Since(start))
			root, data := t.TempDir(), t.TempDir()
			sources := newSourceSet(&localPhotoSource{root: root})
			thumbs := newThumbCache(filepath.Join(data, "thumbs"), filepath.Join(data, "wallpapers"), sources, 1)
			sc := newScanner(config{PhotosDir: root, DataDir: data, ScanEvery: time.Hour}, st, thumbs, sources)
			server := httptest.NewServer(newRouter(st, sc, thumbs, sources, "Asia/Shanghai", testGate()))
			t.Cleanup(server.Close)
			client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{MaxIdleConns: 10, MaxIdleConnsPerHost: 10}}
			t.Cleanup(client.CloseIdleConnections)
			cookie := loginCookie(t, server, "juen", "secret")
			paths := []string{"/api/photos?seed=42&limit=40", "/api/albums", "/v1/backgrounds/random?format=json"}
			for _, path := range paths {
				start := time.Now()
				if err := scaleValidate(client, cookie, server.URL+path, count); err != nil {
					t.Fatal(err)
				}
				t.Logf("cold %s=%s", path, time.Since(start))
			}
			index, err := st.photoIndex()
			if err != nil {
				t.Fatal(err)
			}
			order, err := st.photoOrder(index, 42, "")
			if err != nil {
				t.Fatal(err)
			}
			deep := formatCursor(42, photo{ID: order[count*9/10].id})
			paths = append(paths, "/api/photos?seed=42&limit=40&after="+deep)
			if err := scaleRequest(client, cookie, server.URL+paths[3]); err != nil {
				t.Fatal(err)
			}
			before := []uint64{st.cache.index.builds.Load(), st.cache.orders.builds.Load(), st.cache.albums.builds.Load(), st.cache.walls.builds.Load(), st.cache.wallFilters.builds.Load()}
			for _, path := range paths {
				durations := scaleLoad(t, client, cookie, server.URL+path, 5, 50)
				slices.Sort(durations)
				p50, p95 := durations[len(durations)/2], durations[(len(durations)*95-1)/100]
				t.Logf("hot concurrency=5 requests=%d %s p50=%s p95=%s", len(durations), path, p50, p95)
				if runtime.GOOS == "linux" && p95 > 100*time.Millisecond {
					t.Errorf("hot P95 exceeds the planned 100ms budget: %s %s", path, p95)
				}
			}
			after := []uint64{st.cache.index.builds.Load(), st.cache.orders.builds.Load(), st.cache.albums.builds.Load(), st.cache.walls.builds.Load(), st.cache.wallFilters.builds.Load()}
			if !slices.Equal(before, after) {
				t.Fatalf("unchanged hot requests rebuilt full queries: %v -> %v", before, after)
			}
			if n, err := st.countOK(); err != nil || n != count {
				t.Fatal(n, err)
			}
			runtime.GC()
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			t.Logf("retained heap=%.2fMiB order-cache=%.2fMiB filter-cache=%.2fMiB", float64(mem.HeapAlloc)/(1<<20), float64(st.cache.orders.bytes)/(1<<20), float64(st.cache.wallFilters.bytes)/(1<<20))
			if status, err := os.ReadFile("/proc/self/status"); err == nil {
				for _, line := range strings.Split(string(status), "\n") {
					if strings.HasPrefix(line, "VmRSS:") || strings.HasPrefix(line, "VmHWM:") {
						t.Log(line)
					}
				}
			}
		})
	}
}

func scaleValidate(client *http.Client, cookie *http.Cookie, address string, count int) error {
	request, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.AddCookie(cookie)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", response.StatusCode, body)
	}
	switch {
	case strings.Contains(address, "/api/photos"):
		var page struct {
			Photos []photoItem
			Total  int
			Status scanState
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return err
		}
		if page.Total != count || page.Status.Ready != count || len(page.Photos) != 40 {
			return fmt.Errorf("photo response: total=%d ready=%d photos=%d", page.Total, page.Status.Ready, len(page.Photos))
		}
		seen := make(map[int64]bool)
		for _, p := range page.Photos {
			if p.ID < 1 || p.ID > int64(count) || seen[p.ID] || p.W < 1 || p.H < 1 || p.Size != 5<<20 {
				return fmt.Errorf("invalid or duplicate photo %+v", p)
			}
			seen[p.ID] = true
		}
	case strings.Contains(address, "/api/albums"):
		var page struct {
			Albums []struct {
				ID    string
				Count int
				Cover photoItem
			}
			Total int
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return err
		}
		if page.Total != 1000 || len(page.Albums) != 1000 {
			return fmt.Errorf("album response: %d %d", page.Total, len(page.Albums))
		}
		sum := 0
		for _, a := range page.Albums {
			if a.Count != count/1000 || a.Cover.ID < 1 || a.Cover.ID > int64(count) || photoAlbumID(a.Cover.Name) != a.ID {
				return fmt.Errorf("invalid album %+v", a)
			}
			sum += a.Count
		}
		if sum != count {
			return fmt.Errorf("album sum %d", sum)
		}
	default:
		var reply struct {
			Variant struct {
				Profile       string
				Width, Height int
				Bytes         int64
				SHA256, Path  string
			}
		}
		if err := json.Unmarshal(body, &reply); err != nil {
			return err
		}
		v := reply.Variant
		if (v.Profile != "desktop-3840" && v.Profile != "mobile-1440") || v.Width < 1 || v.Height < 1 || v.Bytes != 100000 || len(v.SHA256) != 64 || !strings.HasSuffix(v.Path, v.SHA256+".webp") {
			return fmt.Errorf("invalid wallpaper %+v", v)
		}
	}
	return nil
}

func populateQueryScale(t *testing.T, st *store, count int) {
	t.Helper()
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	photos, err := tx.Prepare(`INSERT INTO photos(id,rel_path,display_path,size,mtime_unix,width,height,source_version,album_path) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer photos.Close()
	walls, err := tx.Prepare(`INSERT INTO wallpapers(photo_id,profile,source_version,orientation,width,height,bytes,sha256,color,file) VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer walls.Close()
	for id := 1; id <= count; id++ {
		album := fmt.Sprintf("2026/album-%04d", id%1000)
		path := fmt.Sprintf("%s/%08d.jpg", album, id)
		width, height, orientation := 600, 400, "landscape"
		profiles := []string{"desktop-3840"}
		switch id % 3 {
		case 1:
			width, height, orientation = 400, 600, "portrait"
			profiles = []string{"mobile-1440"}
		case 2:
			width, height, orientation = 500, 500, "square"
			profiles = []string{"desktop-3840", "mobile-1440"}
		}
		if _, err := photos.Exec(id, path, path, 5<<20, int64(1700000000+id), width, height, "scale-v1", album); err != nil {
			t.Fatal(err)
		}
		for i, profile := range profiles {
			if _, err := walls.Exec(id, profile, "scale-v1", orientation, width, height, 100000, fmt.Sprintf("%064x", id*2+i), "#123456", fmt.Sprintf("%d-%s.webp", id, profile)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func scaleRequest(client *http.Client, cookie *http.Cookie, address string) error {
	request, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.AddCookie(cookie)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d", address, response.StatusCode)
	}
	return nil
}

func scaleLoad(t *testing.T, client *http.Client, cookie *http.Cookie, address string, workers, requests int) []time.Duration {
	t.Helper()
	results := make(chan time.Duration, workers*requests)
	errors := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < requests; i++ {
				start := time.Now()
				if err := scaleRequest(client, cookie, address); err != nil {
					errors <- err
					return
				}
				results <- time.Since(start)
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	durations := make([]time.Duration, 0, workers*requests)
	for duration := range results {
		durations = append(durations, duration)
	}
	return durations
}
