package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

func modelNames(p albumPeople) []string {
	names := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		names = append(names, m.Name)
	}
	return names
}

func peopleIDs(t *testing.T, st *store) map[string]int64 {
	t.Helper()
	people, err := st.listPeople()
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64)
	for _, p := range people {
		ids[p.role+":"+p.Name] = p.ID
	}
	return ids
}

func TestCleanPersonName(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		{"  Min.E   (민이)\t", "Min.E (민이)"},
		// An ideographic space counts as whitespace too.
		{"LEEHEE\u3000EXPRESS", "LEEHEE EXPRESS"},
		// A decomposed Hangul name, as some file names carry it, is composed.
		{"\u1106\u1175\u11ab", "\ubbfc"},
		{strings.Repeat("名", 40), strings.Repeat("名", 40)},
	} {
		if got, err := cleanPersonName(c.raw); err != nil || got != c.want {
			t.Fatalf("clean %q = %q, %v; want %q", c.raw, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "  \n ", strings.Repeat("名", 41), "a\x00b", "a\u0007b"} {
		if got, err := cleanPersonName(bad); err == nil {
			t.Fatalf("accepted %q as %q", bad, got)
		}
	}
}

func TestAlbumPeopleAssignReplaceClearAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bijin.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.setAlbumPeople([]string{"a", "b"}, strp("LEEHEE EXPRESS"), []string{"G.su", "Min.E (민이)"}); err != nil {
		t.Fatal(err)
	}
	// Names differing only in English letter case are the same person.
	if err := st.setAlbumPeople([]string{"c"}, strp("leehee express"), []string{"g.SU"}); err != nil {
		t.Fatal(err)
	}
	links, err := st.albumPeopleMap()
	if err != nil {
		t.Fatal(err)
	}
	a, c := links["a"], links["c"]
	if a.Author == nil || a.Author.Name != "LEEHEE EXPRESS" || strings.Join(modelNames(a), "|") != "G.su|Min.E (민이)" {
		t.Fatalf("album a %+v", a)
	}
	if c.Author == nil || c.Author.ID != a.Author.ID || c.Author.Name != "LEEHEE EXPRESS" || c.Models[0].ID != a.Models[0].ID {
		t.Fatalf("names should be reused case-insensitively: a %+v c %+v", a, c)
	}
	if people, _ := st.listPeople(); len(people) != 3 {
		t.Fatalf("want 3 people, got %+v", people)
	}

	// A new author keeps the models; nil leaves a field as it is.
	if err := st.setAlbumPeople([]string{"a"}, strp("Pure Media"), nil); err != nil {
		t.Fatal(err)
	}
	// The given model order is kept.
	if err := st.setAlbumPeople([]string{"a"}, nil, []string{"Min.E (민이)", "G.su"}); err != nil {
		t.Fatal(err)
	}
	links, _ = st.albumPeopleMap()
	if a := links["a"]; a.Author.Name != "Pure Media" || strings.Join(modelNames(a), "|") != "Min.E (민이)|G.su" {
		t.Fatalf("album a after replace %+v", a)
	}
	// Empty values clear; other albums keep their people.
	if err := st.setAlbumPeople([]string{"a"}, strp(""), []string{}); err != nil {
		t.Fatal(err)
	}
	links, _ = st.albumPeopleMap()
	if _, ok := links["a"]; ok {
		t.Fatalf("album a should have no people: %+v", links["a"])
	}
	if b := links["b"]; b.Author == nil || b.Author.Name != "LEEHEE EXPRESS" || len(b.Models) != 2 {
		t.Fatalf("album b changed: %+v", b)
	}

	// Everything survives a restart, and opening again does not migrate twice.
	st.Close()
	st, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	links, err = st.albumPeopleMap()
	if err != nil || links["b"].Author == nil || len(links["c"].Models) != 1 {
		t.Fatalf("after reopen %+v %v", links, err)
	}
}

func TestRenameMergeAndDeletePeople(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "bijin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.setAlbumPeople([]string{"x"}, strp("Woo Studio"), []string{"G.su", "Gsu"}); err != nil {
		t.Fatal(err)
	}
	if err := st.setAlbumPeople([]string{"y"}, nil, []string{"Gsu", "Woo"}); err != nil {
		t.Fatal(err)
	}
	ids := peopleIDs(t, st)

	// A plain rename, including a case-only change, keeps the person.
	ref, merged, err := st.renamePerson(ids["model:Woo"], "WOO")
	if err != nil || merged || ref.ID != ids["model:Woo"] || ref.Name != "WOO" {
		t.Fatalf("rename = %+v %v %v", ref, merged, err)
	}
	// An author and a model may share a name; renaming a model to an existing
	// model's name merges them, and the merged person takes the new spelling.
	ref, merged, err = st.renamePerson(ids["model:Gsu"], "g.su")
	if err != nil || !merged || ref.ID != ids["model:G.su"] || ref.Name != "g.su" {
		t.Fatalf("merge = %+v %v %v", ref, merged, err)
	}
	links, _ := st.albumPeopleMap()
	if got := strings.Join(modelNames(links["x"]), "|"); got != "g.su" {
		t.Fatalf("x models %q, want one g.su", got)
	}
	if got := strings.Join(modelNames(links["y"]), "|"); got != "g.su|WOO" {
		t.Fatalf("y models %q, want g.su in Gsu's place", got)
	}
	if people, _ := st.listPeople(); len(people) != 3 {
		t.Fatalf("after merge %+v", people)
	}

	// Deleting a person removes it from every album.
	if err := st.deletePerson(ids["model:Woo"]); err != nil {
		t.Fatal(err)
	}
	links, _ = st.albumPeopleMap()
	if got := strings.Join(modelNames(links["y"]), "|"); got != "g.su" {
		t.Fatalf("y after delete %q", got)
	}
	if err := st.deletePerson(ids["model:Woo"]); !errors.Is(err, errPersonNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if _, _, err := st.renamePerson(9999, "x"); !errors.Is(err, errPersonNotFound) {
		t.Fatalf("rename missing: %v", err)
	}
}

// organizeHarness has the albums "a" (two photos), "b" and "c d".
func organizeHarness(t *testing.T) *settingsHarness {
	t.Helper()
	h := newSettingsHarness(t)
	for i, rel := range []string{"a/2.jpg", "a/1.jpg", "b/1.jpg", "c d/1.jpg"} {
		mtime := int64(1_700_000_000 + i*86400)
		if rel == "a/1.jpg" {
			mtime = 1_600_000_000
		}
		if _, err := h.st.upsert(photo{RelPath: rel, Size: 10, MtimeUnix: mtime, Width: 40, Height: 30}); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *settingsHarness) send(t *testing.T, method, path string, body any, header map[string]string) (int, map[string]any) {
	t.Helper()
	raw := ""
	if body != nil {
		b, _ := json.Marshal(body)
		raw = string(b)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	req.AddCookie(h.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestOrganizeAPIRequiresLoginSameOriginAndJSON(t *testing.T) {
	h := organizeHarness(t)
	routes := [][2]string{{"GET", "/api/people"}, {"PUT", "/api/album-people"}, {"DELETE", "/api/album-people/stale"}, {"PUT", "/api/people/1"}, {"DELETE", "/api/people/1"}}
	for _, r := range routes {
		if code, _ := h.do(t, r[0], r[1], "application/json", "{}", false); code != http.StatusUnauthorized {
			t.Fatalf("%s %s without login = %d", r[0], r[1], code)
		}
	}
	body := `{"albums":["a"],"author":"x"}`
	if code, raw := h.do(t, "PUT", "/api/album-people", "text/plain", body, true); code != http.StatusUnsupportedMediaType {
		t.Fatalf("plain text body = %d %s", code, raw)
	}
	for _, header := range []map[string]string{{"Sec-Fetch-Site": "cross-site"}, {"Origin": "https://evil.example"}, {"Origin": "null"}} {
		for _, r := range routes[1:] {
			code, out := h.send(t, r[0], r[1], map[string]any{"albums": []string{"a"}, "author": "x"}, header)
			if code != http.StatusForbidden || out["error"] != organizeOriginError {
				t.Fatalf("%s %s with %v = %d %v", r[0], r[1], header, code, out)
			}
		}
	}
	// The same origin and same-site requests are fine.
	if code, out := h.send(t, "PUT", "/api/album-people", map[string]any{"albums": []string{"a"}, "author": "x"}, map[string]string{"Origin": h.srv.URL, "Sec-Fetch-Site": "same-origin"}); code != http.StatusOK {
		t.Fatalf("same origin = %d %v", code, out)
	}
}

func TestOrganizeAPIRejectsBadInput(t *testing.T) {
	h := organizeHarness(t)
	many := make([]string, 11)
	for i := range many {
		many[i] = fmt.Sprintf("M%d", i)
	}
	tooManyAlbums := make([]string, maxAlbumsPerRequest+1)
	for i := range tooManyAlbums {
		tooManyAlbums[i] = "a"
	}
	for _, c := range []struct {
		name string
		body any
		want string
	}{
		{"nothing to change", map[string]any{"albums": []string{"a"}}, "没有要修改"},
		{"no albums", map[string]any{"albums": []string{}, "author": "x"}, "1–500"},
		{"too many albums", map[string]any{"albums": tooManyAlbums, "author": "x"}, "1–500"},
		{"unknown album", map[string]any{"albums": []string{"nope"}, "author": "x"}, "不存在"},
		{"traversal", map[string]any{"albums": []string{"../a"}, "author": "x"}, "不存在"},
		{"name too long", map[string]any{"albums": []string{"a"}, "author": strings.Repeat("名", 41)}, "最多 40"},
		{"blank model", map[string]any{"albums": []string{"a"}, "models": []string{" "}}, "不能为空"},
		{"control character", map[string]any{"albums": []string{"a"}, "models": []string{"a\u0007"}}, "控制字符"},
		{"too many models", map[string]any{"albums": []string{"a"}, "models": many}, "最多 10"},
		{"unknown field", map[string]any{"albums": []string{"a"}, "author": "x", "tag": "y"}, "格式"},
		{"wrong type", map[string]any{"albums": "a", "author": "x"}, "格式"},
	} {
		code, out := h.send(t, "PUT", "/api/album-people", c.body, nil)
		if code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(out["error"]), c.want) {
			t.Fatalf("%s = %d %v", c.name, code, out)
		}
	}
	for _, c := range []struct {
		method, path string
		body         any
		code         int
	}{
		{"PUT", "/api/people/abc", map[string]any{"name": "x"}, http.StatusNotFound},
		{"PUT", "/api/people/0", map[string]any{"name": "x"}, http.StatusNotFound},
		{"PUT", "/api/people/77", map[string]any{"name": "x"}, http.StatusNotFound},
		{"DELETE", "/api/people/77", nil, http.StatusNotFound},
	} {
		if code, out := h.send(t, c.method, c.path, c.body, nil); code != c.code {
			t.Fatalf("%s %s = %d %v", c.method, c.path, code, out)
		}
	}
	// Nothing was written by the rejected requests.
	code, out := h.send(t, "GET", "/api/people", nil, nil)
	if code != http.StatusOK || len(out["authors"].([]any)) != 0 || len(out["models"].([]any)) != 0 || out["stale"] != float64(0) {
		t.Fatalf("people after rejected writes = %d %v", code, out)
	}
}

func albumByID(t *testing.T, h *settingsHarness, id string) map[string]any {
	t.Helper()
	code, out := h.send(t, "GET", "/api/albums", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("albums = %d %v", code, out)
	}
	for _, item := range out["albums"].([]any) {
		if album := item.(map[string]any); album["id"] == id {
			return album
		}
	}
	t.Fatalf("album %q missing from %v", id, out)
	return nil
}

func names(list any) string {
	var out []string
	for _, item := range list.([]any) {
		out = append(out, item.(map[string]any)["name"].(string))
	}
	return strings.Join(out, "|")
}

func TestOrganizeAPIAssignRenameDeleteAndStale(t *testing.T) {
	h := organizeHarness(t)
	code, out := h.send(t, "PUT", "/api/album-people", map[string]any{
		"albums": []string{"a", "b", "a"}, "author": " LEEHEE  EXPRESS ", "models": []string{"G.su", "g.SU", "Min.E (민이)"},
	}, nil)
	if code != http.StatusOK {
		t.Fatalf("assign = %d %v", code, out)
	}
	saved := out["albums"].([]any)
	if len(saved) != 2 {
		t.Fatalf("duplicate album ids should be saved once: %v", saved)
	}
	first := saved[0].(map[string]any)
	if first["id"] != "a" || first["author"].(map[string]any)["name"] != "LEEHEE EXPRESS" || names(first["models"]) != "G.su|Min.E (민이)" {
		t.Fatalf("saved album %v", first)
	}
	people := out["people"].(map[string]any)
	if names(people["authors"]) != "LEEHEE EXPRESS" || names(people["models"]) != "G.su|Min.E (민이)" {
		t.Fatalf("people %v", people)
	}
	if people["authors"].([]any)[0].(map[string]any)["albums"] != float64(2) {
		t.Fatalf("author should be used by 2 albums: %v", people)
	}

	// The album list carries the people and the date of the earliest photo.
	a := albumByID(t, h, "a")
	if a["author"].(map[string]any)["name"] != "LEEHEE EXPRESS" || names(a["models"]) != "G.su|Min.E (민이)" ||
		a["added"] != float64(1_600_000_000) || a["addedDate"] != "2020-09-13" {
		t.Fatalf("album a %v", a)
	}
	if cd := albumByID(t, h, "c d"); cd["author"] != nil || len(cd["models"].([]any)) != 0 {
		t.Fatalf("album without people %v", cd)
	}

	// Clearing only the models of b leaves its author.
	if code, out := h.send(t, "PUT", "/api/album-people", map[string]any{"albums": []string{"b"}, "models": []string{}}, nil); code != http.StatusOK {
		t.Fatalf("clear models = %d %v", code, out)
	}
	if b := albumByID(t, h, "b"); b["author"] == nil || len(b["models"].([]any)) != 0 {
		t.Fatalf("album b %v", b)
	}

	// Renaming Min.E to g.su merges into G.su: album a keeps a single entry.
	ids := peopleIDs(t, h.st)
	code, out = h.send(t, "PUT", fmt.Sprintf("/api/people/%d", ids["model:Min.E (민이)"]), map[string]any{"name": " g.su "}, nil)
	if code != http.StatusOK || out["merged"] != true || out["person"].(map[string]any)["name"] != "g.su" {
		t.Fatalf("merge = %d %v", code, out)
	}
	if got := names(albumByID(t, h, "a")["models"]); got != "g.su" {
		t.Fatalf("models after merge %q", got)
	}
	if code, out := h.send(t, "PUT", fmt.Sprintf("/api/people/%d", ids["author:LEEHEE EXPRESS"]), map[string]any{"name": ""}, nil); code != http.StatusBadRequest {
		t.Fatalf("empty rename = %d %v", code, out)
	}

	// Deleting the author removes it from both albums.
	if code, out := h.send(t, "DELETE", fmt.Sprintf("/api/people/%d", ids["author:LEEHEE EXPRESS"]), nil, nil); code != http.StatusOK || len(out["people"].(map[string]any)["authors"].([]any)) != 0 {
		t.Fatalf("delete = %d %v", code, out)
	}
	if a := albumByID(t, h, "a"); a["author"] != nil {
		t.Fatalf("author still on a: %v", a)
	}

	// When an album's folder disappears its people become stale until cleaned.
	gone, ok, err := h.st.getBySourceKey("a/2.jpg")
	if err != nil || !ok {
		t.Fatal(err)
	}
	other, _, _ := h.st.getBySourceKey("a/1.jpg")
	for _, id := range []int64{gone.ID, other.ID} {
		if err := h.st.deleteByID(id); err != nil {
			t.Fatal(err)
		}
	}
	code, out = h.send(t, "GET", "/api/people", nil, nil)
	if code != http.StatusOK || out["stale"] != float64(1) || out["models"].([]any)[0].(map[string]any)["albums"] != float64(0) {
		t.Fatalf("stale view = %d %v", code, out)
	}
	if code, out := h.send(t, "PUT", "/api/album-people", map[string]any{"albums": []string{"a"}, "author": "x"}, nil); code != http.StatusBadRequest {
		t.Fatalf("writing to a removed album = %d %v", code, out)
	}
	code, out = h.send(t, "DELETE", "/api/album-people/stale", nil, nil)
	if code != http.StatusOK || out["removed"] != float64(1) || out["people"].(map[string]any)["stale"] != float64(0) {
		t.Fatalf("clean stale = %d %v", code, out)
	}
	// The name itself stays available for other albums.
	if code, out := h.send(t, "GET", "/api/people", nil, nil); code != http.StatusOK || names(out["models"]) != "g.su" {
		t.Fatalf("people after cleanup = %d %v", code, out)
	}
}
