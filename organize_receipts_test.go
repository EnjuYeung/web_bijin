package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func organizeTestID(at time.Time, serial int) string {
	return fmt.Sprintf("%013d-00000000-0000-4000-8000-%012x", at.UnixMilli(), serial)
}

func applyOrganizeForTest(t *testing.T, st *store, in organizeChange, current map[string]bool, now time.Time) organizeResult {
	t.Helper()
	raw, err := st.applyOrganize(in, current, now)
	if err != nil {
		t.Fatal(err)
	}
	var out organizeResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOrganizeReceiptsConfirmOnceAcrossRestartAndLaterChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bijin.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	current := map[string]bool{"a": true, "b": true}
	models := []string{"A", "B"}
	assign := organizeChange{OperationID: organizeTestID(now, 1), Kind: "assign", Albums: []string{"a", "b"}, Models: &models}
	first := applyOrganizeForTest(t, st, assign, current, now)
	if len(first.Albums) != 2 || len(first.People.Models) != 2 {
		t.Fatalf("assignment %+v", first)
	}
	aID := peopleIDs(t, st)["model:A"]
	rename := organizeChange{OperationID: organizeTestID(now, 2), Kind: "rename", PersonID: aID, Name: "Renamed"}
	renamed := applyOrganizeForTest(t, st, rename, current, now)
	if renamed.Person == nil || renamed.Person.Name != "Renamed" {
		t.Fatalf("rename %+v", renamed)
	}
	// A subsequent edit must not be overwritten by a retried, earlier rename.
	if _, _, err := st.renamePerson(aID, "Latest"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	duplicate := applyOrganizeForTest(t, st, rename, current, now)
	if duplicate.Person.Name != "Renamed" {
		t.Fatalf("did not retain original receipt: %+v", duplicate)
	}
	links, err := st.albumPeopleMap()
	if err != nil || modelNames(links["a"])[0] != "Latest" {
		t.Fatalf("retry overwrote later edit: %+v %v", links, err)
	}
	remove := organizeChange{OperationID: organizeTestID(now, 3), Kind: "remove", PersonID: aID}
	applyOrganizeForTest(t, st, remove, current, now)
	if err := st.setAlbumPeople([]string{"b"}, nil, []string{"Latest"}); err != nil {
		t.Fatal(err)
	}
	applyOrganizeForTest(t, st, remove, current, now)
	links, err = st.albumPeopleMap()
	if err != nil || strings.Join(modelNames(links["b"]), "|") != "Latest" {
		t.Fatalf("delete retried against a later name: %+v %v", links, err)
	}
	var count int
	if err := st.db.QueryRow(`SELECT count(*) FROM organize_receipts`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("receipts = %d %v", count, err)
	}
}

func TestOrganizeReceiptsMergeAndConcurrentDuplicates(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.setAlbumPeople([]string{"a"}, nil, []string{"Source", "Middle", "Target"}); err != nil {
		t.Fatal(err)
	}
	ids := peopleIDs(t, st)
	now := time.Now()
	in := organizeChange{OperationID: organizeTestID(now, 1), Kind: "rename", PersonID: ids["model:Source"], Name: "target"}
	var wg sync.WaitGroup
	results := make(chan []byte, 16)
	errors := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := st.applyOrganize(in, map[string]bool{"a": true}, now)
			if err != nil {
				errors <- err
			} else {
				results <- raw
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	var first []byte
	for raw := range results {
		if first == nil {
			first = raw
		} else if !bytes.Equal(first, raw) {
			t.Fatal("duplicate response differs")
		}
	}
	links, err := st.albumPeopleMap()
	if err != nil || strings.Join(modelNames(links["a"]), "|") != "Middle|target" {
		t.Fatalf("merge positions %+v %v", links, err)
	}
	var count int
	_ = st.db.QueryRow(`SELECT count(*) FROM organize_receipts`).Scan(&count)
	if count != 1 {
		t.Fatalf("%d receipts for one operation", count)
	}
}

func TestOrganizeReceiptsRollBackWhenReceiptCannotCommitAndRejectChangedContent(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	in := organizeChange{OperationID: organizeTestID(now, 1), Kind: "assign", Albums: []string{"a"}, Author: strp("First")}
	if _, err := st.db.Exec(`CREATE TRIGGER refuse_receipt BEFORE INSERT ON organize_receipts BEGIN SELECT RAISE(ABORT, 'test receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.applyOrganize(in, map[string]bool{"a": true}, now); err == nil {
		t.Fatal("expected receipt failure")
	}
	links, err := st.albumPeopleMap()
	if err != nil || len(links) != 0 {
		t.Fatalf("data committed without receipt: %+v %v", links, err)
	}
	if people, _ := st.listPeople(); len(people) != 0 {
		t.Fatalf("name committed without receipt: %+v", people)
	}
	if _, err := st.db.Exec(`DROP TRIGGER refuse_receipt`); err != nil {
		t.Fatal(err)
	}
	applyOrganizeForTest(t, st, in, map[string]bool{"a": true}, now)
	in.Author = strp("Changed payload")
	if _, err := st.applyOrganize(in, map[string]bool{"a": true}, now); err == nil {
		t.Fatal("accepted changed content with the same ID")
	}
	links, _ = st.albumPeopleMap()
	if links["a"].Author.Name != "First" {
		t.Fatalf("changed payload executed: %+v", links)
	}
}

type lostOrganizeAnswer struct{ header http.Header }

func (w *lostOrganizeAnswer) Header() http.Header     { return w.header }
func (*lostOrganizeAnswer) WriteHeader(int)           {}
func (*lostOrganizeAnswer) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestOrganizeReceiptAfterLostHTTPAnswerAndAuthentication(t *testing.T) {
	h := organizeHarness(t)
	now := time.Now()
	id := organizeTestID(now, 1)
	body := organizeChange{OperationID: id, Kind: "assign", Albums: []string{"a"}, Author: strp("Saved despite disconnection")}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, h.srv.URL+"/api/organize", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	(&organizeAPI{store: h.st}).change(&lostOrganizeAnswer{header: make(http.Header)}, req)
	if author := albumByID(t, h, "a")["author"].(map[string]any)["name"]; author != *body.Author {
		t.Fatalf("lost answer lost commit: %v", author)
	}
	code, receipt := h.send(t, "GET", "/api/organize/receipts/"+id, nil, nil)
	if code != http.StatusOK || receipt["state"] != "committed" {
		t.Fatalf("receipt %d %+v", code, receipt)
	}
	if receipt["result"].(map[string]any)["operationId"] != id {
		t.Fatalf("wrong receipt %+v", receipt)
	}
	code, result := h.send(t, "POST", "/api/organize", body, nil)
	if code != http.StatusOK || result["operationId"] != id {
		t.Fatalf("retry %d %+v", code, result)
	}
	code, receipt = h.send(t, "GET", "/api/organize/receipts/"+organizeTestID(now, 2), nil, nil)
	if code != http.StatusOK || receipt["state"] != "missing" {
		t.Fatalf("missing receipt %d %+v", code, receipt)
	}
	for _, route := range []struct{ method, path string }{{"POST", "/api/organize"}, {"GET", "/api/organize/receipts/" + id}} {
		if code, _ := h.do(t, route.method, route.path, "application/json", string(raw), false); code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %d", route.path, code)
		}
	}
	for _, headers := range []map[string]string{{"Origin": "https://elsewhere.invalid"}, {"Origin": "null"}, {"Sec-Fetch-Site": "cross-site"}} {
		if code, _ := h.send(t, "POST", "/api/organize", body, headers); code != http.StatusForbidden {
			t.Fatalf("cross-site %d", code)
		}
	}
	if code, _ := h.do(t, "POST", "/api/organize", "text/plain", string(raw), true); code != http.StatusUnsupportedMediaType {
		t.Fatalf("plain text %d", code)
	}
}

func TestOrganizeReceiptsValidateInputsAndCleanStale(t *testing.T) {
	h := organizeHarness(t)
	now := time.Now()
	badModels := []string{strings.Repeat("名", 41)}
	for i, body := range []organizeChange{
		{Kind: "assign", Albums: []string{"a"}},
		{Kind: "assign", Albums: []string{"absent"}, Author: strp("x")},
		{Kind: "assign", Albums: []string{"a"}, Models: &badModels},
		{Kind: "rename", PersonID: 999, Name: "Missing"},
		{Kind: "remove", PersonID: 999},
		{Kind: "noSuchAction"},
	} {
		body.OperationID = organizeTestID(now, i+1)
		code, _ := h.send(t, "POST", "/api/organize", body, nil)
		if code != http.StatusBadRequest && code != http.StatusNotFound {
			t.Fatalf("accepted bad input: %+v = %d", body, code)
		}
	}
	if people, _ := h.st.listPeople(); len(people) != 0 {
		t.Fatalf("bad requests wrote names %+v", people)
	}
	if err := h.st.setAlbumPeople([]string{"gone"}, strp("Unused"), nil); err != nil {
		t.Fatal(err)
	}
	code, out := h.send(t, "POST", "/api/organize", organizeChange{OperationID: organizeTestID(now, 10), Kind: "cleanStale"}, nil)
	if code != http.StatusOK || out["removed"] != float64(1) || out["people"].(map[string]any)["stale"] != float64(0) {
		t.Fatalf("cleanup %d %+v", code, out)
	}
	if people, _ := h.st.listPeople(); len(people) != 1 {
		t.Fatalf("cleanup removed name %+v", people)
	}
}

func TestOrganizeReceiptsBoundRetentionAndNeverReplayPrunedIDs(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	current := map[string]bool{"a": true}
	first := organizeChange{OperationID: organizeTestID(now.Add(-time.Second), 1), Kind: "assign", Albums: []string{"a"}, Author: strp("Original")}
	applyOrganizeForTest(t, st, first, current, now)
	for i := 1; i <= organizeReceiptLimit; i++ {
		in := organizeChange{OperationID: organizeTestID(now.Add(time.Duration(i)*time.Millisecond), i+1), Kind: "assign", Albums: []string{"a"}, Author: strp("Latest")}
		applyOrganizeForTest(t, st, in, current, now)
	}
	if _, err := st.applyOrganize(first, current, now); !errors.Is(err, errOrganizeReceiptExpired) {
		t.Fatalf("pruned ID accepted: %v", err)
	}
	links, _ := st.albumPeopleMap()
	if links["a"].Author.Name != "Latest" {
		t.Fatalf("pruned ID replayed: %+v", links)
	}
	var count, size int
	_ = st.db.QueryRow(`SELECT count(*), COALESCE(sum(length(result)), 0) FROM organize_receipts`).Scan(&count, &size)
	if count > organizeReceiptLimit || size > organizeReceiptBytes {
		t.Fatalf("unbounded receipts: %d / %d", count, size)
	}
	old := organizeChange{OperationID: organizeTestID(now.Add(-organizeReceiptTTL-time.Second), 999), Kind: "assign", Albums: []string{"a"}, Author: strp("Old")}
	if _, err := st.applyOrganize(old, current, now); !errors.Is(err, errOrganizeReceiptExpired) {
		t.Fatalf("expired ID accepted: %v", err)
	}
	// Enforce the byte bound independently of the count limit.
	if _, err := st.db.Exec(`UPDATE organize_receipts SET result = zeroblob(?) WHERE operation_id IN (SELECT operation_id FROM organize_receipts ORDER BY issued_ms DESC LIMIT 2)`, organizeReceiptBytes); err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := pruneOrganizeReceipts(tx, now); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = st.db.QueryRow(`SELECT count(*), COALESCE(sum(length(result)), 0) FROM organize_receipts`).Scan(&count, &size)
	if count > 1 || size > organizeReceiptBytes {
		t.Fatalf("byte limit not enforced: %d / %d", count, size)
	}
}

type slowOrganizeAnswer struct {
	header  http.Header
	started chan struct{}
	release chan struct{}
}

func (w *slowOrganizeAnswer) Header() http.Header { return w.header }
func (*slowOrganizeAnswer) WriteHeader(int)       {}
func (w *slowOrganizeAnswer) Write(raw []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(raw), nil
}

func TestOrganizeReceiptSlowReaderDoesNotBlockLaterSaves(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	id := organizeTestID(now, 1)
	applyOrganizeForTest(t, st, organizeChange{OperationID: id, Kind: "assign", Albums: []string{"a"}, Author: strp("First")}, map[string]bool{"a": true}, now)
	w := &slowOrganizeAnswer{header: make(http.Header), started: make(chan struct{}), release: make(chan struct{})}
	r := httptest.NewRequest("GET", "/api/organize/receipts/"+id, nil)
	r.SetPathValue("operation", id)
	finished := make(chan struct{})
	go func() { (&organizeAPI{store: st}).receipt(w, r); close(finished) }()
	<-w.started
	saved := make(chan error, 1)
	go func() { saved <- st.setAlbumPeople([]string{"a"}, strp("Later"), nil) }()
	select {
	case err := <-saved:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Error("sending a receipt held the SQLite connection and blocked the save")
	}
	close(w.release)
	<-finished
}
