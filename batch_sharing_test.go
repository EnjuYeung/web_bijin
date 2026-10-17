package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func batchCall(t *testing.T, h *shareHarness, method, path string, body any) shareBatch {
	t.Helper()
	res, raw := shareRequest(t, h.srv, h.cookie, method, path, body, nil)
	if res.StatusCode != 200 {
		t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
	}
	var b shareBatch
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b
}
func prepareBatch(t *testing.T, h *shareHarness, mode string, albums ...string) shareBatch {
	return batchCall(t, h, "POST", "/api/share-batches/prepare", map[string]any{"mode": mode, "albums": albums})
}
func startBatch(t *testing.T, h *shareHarness, b shareBatch) shareBatch {
	return batchCall(t, h, "POST", "/api/share-batches/"+b.ID+"/start", map[string]any{"revision": b.Revision})
}
func waitBatch(t *testing.T, h *shareHarness, id string) shareBatch {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b := batchCall(t, h, "GET", "/api/share-batches/"+id, nil)
		if b.Status != "running" {
			return b
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("batch did not finish")
	return shareBatch{}
}
func TestBatchSharingScopeOriginalReuseStopAndLinks(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"A/a.jpg": tinyJPEG(20, 16), "A/sub/child.jpg": tinyJPEG(16, 20), "B/b.jpg": tinyJPEG(18, 18)})
	old := h.share(t, "A/a.jpg")
	b := prepareBatch(t, h, "open", "A", "B")
	if b.Total != 2 || b.Shared != 1 || b.Changes != 1 {
		t.Fatalf("scope %+v", b)
	}
	// A later addition cannot silently expand the accepted scope.
	if err := os.WriteFile(filepath.Join(h.root, "A/new.jpg"), tinyJPEG(18, 20), 0644); err != nil {
		t.Fatal(err)
	}
	h.sc.run(context.Background())
	startBatch(t, h, b)
	done := waitBatch(t, h, b.ID)
	if done.Succeeded != 2 || done.Failed != 0 {
		t.Fatalf("result %+v", done)
	}
	if done.Items[0].Path != old.Path {
		t.Fatal("existing identity changed")
	}
	for _, name := range []string{"A/sub/child.jpg", "A/new.jpg"} {
		sh, err := h.st.shareForPhoto(h.photo(t, name).ID)
		if err != nil || sh.Token != "" {
			t.Fatal("outside snapshot shared", name, sh, err)
		}
	}
	for _, it := range done.Items {
		res, raw := shareRequest(t, h.srv, nil, "GET", it.Path, nil, nil)
		if res.StatusCode != 200 {
			t.Fatal(res.StatusCode)
		}
		p := h.photo(t, it.Name)
		want, err := os.ReadFile(filepath.Join(h.root, p.RelPath))
		if err != nil || !bytes.Equal(raw, want) {
			t.Fatal("not original bytes")
		}
	}
	res, raw := shareRequest(t, h.srv, h.cookie, "GET", "/api/share-batches/"+b.ID+"/links", nil, nil)
	var links struct{ Paths []string }
	json.Unmarshal(raw, &links)
	if res.StatusCode != 200 || len(links.Paths) != 2 {
		t.Fatal(string(raw))
	}
	stop := prepareBatch(t, h, "stop", "A", "B")
	startBatch(t, h, stop)
	doneStop := waitBatch(t, h, stop.ID)
	if doneStop.Failed != 0 {
		t.Fatal(doneStop)
	}
	for _, it := range done.Items {
		res, _ = shareRequest(t, h.srv, nil, "GET", it.Path, nil, nil)
		if res.StatusCode != 404 {
			t.Fatal("not stopped", it)
		}
	}
	res, raw = shareRequest(t, h.srv, h.cookie, "GET", "/api/share-batches/"+b.ID+"/links", nil, nil)
	links.Paths = nil
	if err := json.Unmarshal(raw, &links); err != nil || res.StatusCode != 200 || len(links.Paths) != 0 {
		t.Fatal("stopped links copied")
	}
}
func TestBatchSharingFailureRetrySingleIntentAndIdempotence(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"A/a.jpg": tinyJPEG(20, 16), "A/b.jpg": tinyJPEG(16, 20)})
	b := prepareBatch(t, h, "open", "A")
	path := filepath.Join(h.root, "A/a.jpg")
	hidden := path + ".offline"
	os.Rename(path, hidden)
	startBatch(t, h, b)
	done := waitBatch(t, h, b.ID)
	if done.Succeeded != 1 || done.Failed != 1 {
		t.Fatalf("mixed %+v", done)
	}
	old := done.Items[1].Path
	os.Rename(hidden, path)
	retry := batchCall(t, h, "POST", "/api/share-batches/"+b.ID+"/retry", map[string]any{"revision": done.Revision})
	done = waitBatch(t, h, retry.ID)
	if done.Succeeded != 2 || done.Items[1].Path != old {
		t.Fatal("retry changed success", done)
	}
	// Repeating an older attempt never retries or creates another permission.
	duplicate := batchCall(t, h, "POST", "/api/share-batches/"+b.ID+"/retry", map[string]any{"revision": b.Revision + 1})
	if duplicate.Revision != done.Revision {
		t.Fatal("duplicate attempt")
	}
	stop := prepareBatch(t, h, "stop", "A")
	p := h.photo(t, "A/a.jpg")
	sh, _ := h.st.shareForPhoto(p.ID)
	h.st.stopShare(p.ID, sh.Generation)
	newShare := h.share(t, "A/a.jpg")
	startBatch(t, h, stop)
	done = waitBatch(t, h, stop.ID)
	if done.Failed != 1 {
		t.Fatal("newer single intent overwritten", done)
	}
	res, _ := shareRequest(t, h.srv, nil, "GET", newShare.Path, nil, nil)
	if res.StatusCode != 200 {
		t.Fatal("newer sharing lost")
	}
}
func TestBatchSharingCancelSingleTaskAndRestart(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"A/a.jpg": tinyJPEG(20, 16), "A/b.jpg": tinyJPEG(16, 20)})
	blocked, release := make(chan struct{}), make(chan struct{})
	controlled := &shareControlledSource{localPhotoSource: &localPhotoSource{root: h.root}, hook: func() { close(blocked); <-release }}
	h.sources.local = controlled
	b := prepareBatch(t, h, "open", "A")
	running := startBatch(t, h, b)
	<-blocked
	second := prepareBatch(t, h, "stop", "A")
	res, _ := shareRequest(t, h.srv, h.cookie, "POST", "/api/share-batches/"+second.ID+"/start", map[string]any{"revision": second.Revision}, nil)
	if res.StatusCode != 409 {
		t.Fatal("second batch ran")
	}
	cancelled := batchCall(t, h, "POST", "/api/share-batches/"+b.ID+"/cancel", map[string]any{"revision": running.Revision})
	if cancelled.Status != "cancelled" {
		t.Fatal(cancelled)
	}
	close(release)
	time.Sleep(30 * time.Millisecond)
	for _, name := range []string{"A/a.jpg", "A/b.jpg"} {
		sh, _ := h.st.shareForPhoto(h.photo(t, name).ID)
		if sh.Token != "" {
			t.Fatal("cancelled task committed")
		}
	}
	// Reopen the actual SQLite database after a persisted partial attempt.
	h.sources.local = &localPhotoSource{root: h.root}
	retry := batchCall(t, h, "POST", "/api/share-batches/"+b.ID+"/retry", map[string]any{"revision": running.Revision})
	done := waitBatch(t, h, retry.ID)
	if done.Succeeded != 2 {
		t.Fatal(done)
	}
	next := prepareBatch(t, h, "stop", "A")
	if _, err := h.st.db.Exec("UPDATE share_batches SET status='running',revision=1 WHERE id=?", next.ID); err != nil {
		t.Fatal(err)
	}
	h.srv.Close()
	h.st.Close()
	h.start(t)
	persisted := batchCall(t, h, "GET", "/api/share-batches/"+next.ID, nil)
	if persisted.Status != "interrupted" || persisted.Done != 0 {
		t.Fatal("restart auto continued", persisted)
	}
	for _, it := range done.Items {
		res, _ := shareRequest(t, h.srv, nil, "GET", it.Path, nil, nil)
		if res.StatusCode != 200 {
			t.Fatal("restart lost permit")
		}
	}
}
func TestBatchSharingGateExpiryReplacementAndSummary(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"A/a.jpg": tinyJPEG(20, 16)})
	for _, method := range []string{"GET", "POST"} {
		path := "/api/share-batches"
		var body any
		if method == "POST" {
			path += "/prepare"
			body = map[string]any{"mode": "open", "albums": []string{"A"}}
		}
		res, _ := shareRequest(t, h.srv, nil, method, path, body, nil)
		if res.StatusCode != 401 {
			t.Fatal("no login", res.StatusCode)
		}
	}
	for _, albums := range [][]string{{"../A"}, {"A", "missing"}, {}} {
		res, _ := shareRequest(t, h.srv, h.cookie, "POST", "/api/share-batches/prepare", map[string]any{"mode": "open", "albums": albums}, nil)
		if res.StatusCode == 200 {
			t.Fatal("bad scope")
		}
	}
	res, _ := shareRequest(t, h.srv, h.cookie, "POST", "/api/share-batches/prepare", map[string]any{"mode": "open", "albums": []string{"A"}}, map[string]string{"Origin": "https://outside.invalid"})
	if res.StatusCode != 403 {
		t.Fatal("foreign origin")
	}
	b := prepareBatch(t, h, "open", "A")
	os.WriteFile(filepath.Join(h.root, "A/a.jpg"), tinyJPEG(16, 20), 0644)
	startBatch(t, h, b)
	done := waitBatch(t, h, b.ID)
	if done.Failed != 1 {
		t.Fatal("replacement shared", done)
	}
	h.sc.run(context.Background())
	fresh := prepareBatch(t, h, "open", "A")
	startBatch(t, h, fresh)
	done = waitBatch(t, h, fresh.ID)
	res, raw := shareRequest(t, h.srv, h.cookie, "GET", "/api/albums", nil, nil)
	if res.StatusCode != 200 || !bytes.Contains(raw, []byte(`"shared":1`)) {
		t.Fatal("summary stale", string(raw))
	}
	h.st.db.Exec("UPDATE share_batches SET finished=? WHERE id=?", time.Now().Add(-49*time.Hour).Unix(), fresh.ID)
	res, _ = shareRequest(t, h.srv, h.cookie, "GET", "/api/share-batches/"+fresh.ID, nil, nil)
	if res.StatusCode != 404 {
		t.Fatal("history not expired")
	}
	res, _ = shareRequest(t, h.srv, nil, http.MethodGet, done.Items[0].Path, nil, nil)
	if res.StatusCode != 200 {
		t.Fatal("expired task revoked permit")
	}
}

// The page polls a running task every two seconds: it gets the counts and the
// failures, and every photo only once the task has stopped.
func TestBatchSharingRunningDetailsCarryFailuresOnly(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"A/a.jpg": tinyJPEG(12, 12), "A/b.jpg": tinyJPEG(14, 12)})
	b := prepareBatch(t, h, "open", "A")
	if len(b.Items) != 2 {
		t.Fatal("prepared items", b.Items)
	}
	h.st.db.Exec("UPDATE share_batches SET status='running',revision=1 WHERE id=?", b.ID)
	h.st.db.Exec("UPDATE share_batch_items SET status='failed',error='x' WHERE batch_id=? AND ordinal=1", b.ID)
	running := batchCall(t, h, "GET", "/api/share-batches/"+b.ID, nil)
	if running.Total != 2 || running.Failed != 1 || running.Done != 1 || len(running.Items) != 1 || running.Items[0].Status != "failed" {
		t.Fatalf("running details %+v", running)
	}
	h.st.db.Exec("UPDATE share_batches SET status='cancelled',finished=? WHERE id=?", time.Now().Unix(), b.ID)
	stopped := batchCall(t, h, "GET", "/api/share-batches/"+b.ID, nil)
	if len(stopped.Items) != 2 || stopped.Failed != 1 {
		t.Fatalf("stopped details %+v", stopped)
	}
}

func TestBatchSharingSnapshotBeyondPagination(t *testing.T) {
	files := map[string][]byte{}
	for i := 0; i < 85; i++ {
		files[fmt.Sprintf("A/%03d.jpg", i)] = tinyJPEG(8, 8)
	}
	h := newShareHarness(t, files)
	b := prepareBatch(t, h, "open", "A")
	if b.Total != 85 {
		t.Fatal(b.Total)
	}
	startBatch(t, h, b)
	done := waitBatch(t, h, b.ID)
	if done.Succeeded != 85 {
		t.Fatal(done.Succeeded, done.Failed)
	}
}

func TestBatchSharingOldAttemptCannotCommitOrInterruptRetry(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"A/a.jpg": tinyJPEG(12, 12)})
	b := prepareBatch(t, h, "open", "A")
	if _, err := h.st.db.Exec("UPDATE share_batches SET status='running',revision=2 WHERE id=?", b.ID); err != nil {
		t.Fatal(err)
	}
	a := &batchAPI{share: newShareAPI(h.st, h.sources, t.TempDir()), now: time.Now}
	before, err := h.st.readBatch(b.ID, batchAll)
	if err != nil {
		t.Fatal(err)
	}
	it := before.Items[0]
	if err = a.commitItem(b.ID, 1, "open", it, photoShare{}, nil); err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), b.ID, 1)
	after, err := h.st.readBatch(b.ID, batchAll)
	if err != nil || after.Status != "running" || after.Revision != 2 || after.Done != 0 {
		t.Fatal("old attempt changed retry", after, err)
	}
	sh, _ := h.st.shareForPhoto(it.ID)
	if sh.Token != "" {
		t.Fatal("stale attempt created permit")
	}
	h.st.db.Exec("UPDATE share_batches SET status='cancelled' WHERE id=?", b.ID)
}
func TestBatchSharingMergedLocalAndS3Sources(t *testing.T) {
	f, st, th, sc := s3Harness(t, 2, nil)
	local := &localPhotoSource{root: t.TempDir()}
	sc.sources.local = local
	os.MkdirAll(filepath.Join(local.root, "A"), 0755)
	a, b := tinyJPEG(12, 16), tinyJPEG(16, 12)
	os.WriteFile(filepath.Join(local.root, "A/a.jpg"), a, 0644)
	f.put("A/a.jpg", b)
	sc.run(context.Background())
	srv := httptest.NewServer(newRouter(st, sc, th, sc.sources, "Asia/Shanghai", testGate()))
	defer srv.Close()
	h := &shareHarness{st: st, srv: srv, cookie: loginCookie(t, srv, "juen", "secret")}
	task := prepareBatch(t, h, "open", "A")
	if task.Total != 2 {
		t.Fatal("sources not merged", task.Total)
	}
	startBatch(t, h, task)
	done := waitBatch(t, h, task.ID)
	if done.Succeeded != 2 {
		t.Fatal(done)
	}
	seen := map[string]bool{}
	for _, it := range done.Items {
		res, raw := shareRequest(t, srv, nil, "GET", it.Path, nil, nil)
		if res.StatusCode != 200 {
			t.Fatal(res.StatusCode)
		}
		seen[string(raw)] = true
	}
	if !seen[string(a)] || !seen[string(b)] {
		t.Fatal("wrong source bytes")
	}
}

func TestBatchSharingPermitAndResultRollbackTogether(t *testing.T) {
	h := newShareHarness(t, map[string][]byte{"A/a.jpg": tinyJPEG(12, 12)})
	b := prepareBatch(t, h, "open", "A")
	h.st.db.Exec("UPDATE share_batches SET status='running',revision=1 WHERE id=?", b.ID)
	a := &batchAPI{share: newShareAPI(h.st, h.sources, t.TempDir()), now: time.Now}
	selected, err := h.st.readBatch(b.ID, batchAll)
	if err != nil {
		t.Fatal(err)
	}
	it := selected.Items[0]
	snap, err := a.share.snapshot(context.Background(), it.Photo)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.file.Close()
	fresh := photoShare{Token: strings.Repeat("A", 43), SourceKey: it.Photo.sourceKey(), SourceVersion: it.Photo.SourceVersion, Scope: snap.scope, Digest: snap.digest, MIME: snap.mime, Extension: snap.extension, Size: snap.size}
	_, err = h.st.db.Exec(`CREATE TRIGGER batch_result_failure BEFORE UPDATE OF status ON share_batch_items WHEN NEW.status='success' BEGIN SELECT RAISE(ABORT,'fixture result failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.commitItem(b.ID, 1, "open", it, fresh, nil); err == nil {
		t.Fatal("fixture did not reject result")
	}
	sh, err := h.st.shareForPhoto(it.ID)
	if err != nil || sh.Token != "" || sh.Generation != it.Generation {
		t.Fatal("permit committed without result", sh, err)
	}
	after, err := h.st.readBatch(b.ID, batchAll)
	if err != nil || after.Done != 0 {
		t.Fatal("uncommitted result displayed", after, err)
	}
	h.st.db.Exec("UPDATE share_batches SET status='cancelled' WHERE id=?", b.ID)
}
