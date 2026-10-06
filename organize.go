package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Album authors and models. Albums are still aggregated from photo folders on
// photo changes; only the people of a folder are stored, keyed by the album id
// (its full relative directory). A renamed folder leaves its people behind as
// stale entries until they are cleaned up on the organize page.

const (
	roleAuthor          = "author"
	roleModel           = "model"
	maxPersonName       = 40
	maxModelsPerAlbum   = 10
	maxAlbumsPerRequest = 500
	organizeOriginError = "只能从相册网站整理"
)

var errPersonNotFound = errors.New("person not found")

type personRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// albumPeople is the author and the ordered models of one album.
type albumPeople struct {
	Author *personRef  `json:"author"`
	Models []personRef `json:"models"`
}

type personRow struct {
	personRef
	role string
}

func (s *store) migratePeople() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS people (
  id INTEGER PRIMARY KEY,
  role TEXT NOT NULL,
  name TEXT NOT NULL COLLATE NOCASE,
  UNIQUE (role, name)
);
CREATE TABLE IF NOT EXISTS album_people (
  album TEXT NOT NULL,
  person_id INTEGER NOT NULL,
  position INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (album, person_id)
);
CREATE INDEX IF NOT EXISTS album_people_person ON album_people(person_id);
`)
	return err
}

// cleanPersonName trims a name, joins inner whitespace and composes Unicode,
// so a Hangul name typed in the browser matches one copied from a file name.
func cleanPersonName(raw string) (string, error) {
	name := norm.NFC.String(strings.Join(strings.Fields(raw), " "))
	switch {
	case name == "":
		return "", invalid("名字不能为空")
	case utf8.RuneCountInString(name) > maxPersonName:
		return "", invalid("名字最多 %d 个字", maxPersonName)
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return "", invalid("名字不能包含控制字符")
	}
	return name, nil
}

// nameKey folds ASCII letters only, matching SQLite's NOCASE on people.name.
func nameKey(name string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, name)
}

func personID(tx *sql.Tx, role, name string) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM people WHERE role = ? AND name = ?`, role, name).Scan(&id)
	if err != sql.ErrNoRows {
		return id, err
	}
	res, err := tx.Exec(`INSERT INTO people (role, name) VALUES (?, ?)`, role, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// setAlbumPeople replaces the author and/or the models of the albums in one
// transaction. A nil author or nil models leaves that field as it is; an empty
// author or an empty model list clears it. Missing names are created.
func (s *store) setAlbumPeople(albums []string, author *string, models []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var authorIDs []int64
	if author != nil && *author != "" {
		id, err := personID(tx, roleAuthor, *author)
		if err != nil {
			return err
		}
		authorIDs = []int64{id}
	}
	modelIDs := make([]int64, 0, len(models))
	for _, name := range models {
		id, err := personID(tx, roleModel, name)
		if err != nil {
			return err
		}
		modelIDs = append(modelIDs, id)
	}
	replace := func(album, role string, ids []int64) error {
		if _, err := tx.Exec(`DELETE FROM album_people WHERE album = ? AND person_id IN (SELECT id FROM people WHERE role = ?)`, album, role); err != nil {
			return err
		}
		for i, id := range ids {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO album_people (album, person_id, position) VALUES (?, ?, ?)`, album, id, i); err != nil {
				return err
			}
		}
		return nil
	}
	for _, album := range albums {
		if author != nil {
			if err := replace(album, roleAuthor, authorIDs); err != nil {
				return err
			}
		}
		if models != nil {
			if err := replace(album, roleModel, modelIDs); err != nil {
				return err
			}
		}
	}
	return s.commitPeople(tx)
}

// albumPeopleMap returns the people of every album that has any, including
// albums whose folder no longer exists.
func (s *store) albumPeopleMap() (map[string]albumPeople, error) {
	return s.cache.people.get(&s.cache.peopleRevision, func(uint64) (map[string]albumPeople, error) {
		return s.loadAlbumPeople()
	})
}

func (s *store) loadAlbumPeople() (map[string]albumPeople, error) {
	rows, err := s.db.Query(`SELECT ap.album, p.id, p.role, p.name FROM album_people ap
	  JOIN people p ON p.id = ap.person_id ORDER BY ap.album, ap.position, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]albumPeople)
	for rows.Next() {
		var album, role string
		var ref personRef
		if err := rows.Scan(&album, &ref.ID, &role, &ref.Name); err != nil {
			return nil, err
		}
		people := out[album]
		if role == roleAuthor {
			people.Author = &ref
		} else {
			people.Models = append(people.Models, ref)
		}
		out[album] = people
	}
	return out, rows.Err()
}

func (s *store) listPeople() ([]personRow, error) {
	rows, err := s.db.Query(`SELECT id, role, name FROM people ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []personRow
	for rows.Next() {
		var p personRow
		if err := rows.Scan(&p.ID, &p.role, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// renamePerson renames a person. When another person of the same role already
// has that name, the two are merged into it and it takes the new spelling;
// an album that had both keeps one entry.
func (s *store) renamePerson(id int64, name string) (personRef, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return personRef{}, false, err
	}
	defer tx.Rollback()
	var role string
	err = tx.QueryRow(`SELECT role FROM people WHERE id = ?`, id).Scan(&role)
	if err == sql.ErrNoRows {
		return personRef{}, false, errPersonNotFound
	}
	if err != nil {
		return personRef{}, false, err
	}
	target := id
	err = tx.QueryRow(`SELECT id FROM people WHERE role = ? AND name = ? AND id != ?`, role, name, id).Scan(&target)
	if err != nil && err != sql.ErrNoRows {
		return personRef{}, false, err
	}
	merged := err == nil
	if merged {
		if _, err := tx.Exec(`UPDATE OR IGNORE album_people SET person_id = ? WHERE person_id = ?`, target, id); err != nil {
			return personRef{}, false, err
		}
		if _, err := tx.Exec(`DELETE FROM album_people WHERE person_id = ?`, id); err != nil {
			return personRef{}, false, err
		}
		if _, err := tx.Exec(`DELETE FROM people WHERE id = ?`, id); err != nil {
			return personRef{}, false, err
		}
	}
	if _, err := tx.Exec(`UPDATE people SET name = ? WHERE id = ?`, name, target); err != nil {
		return personRef{}, false, err
	}
	if err := s.commitPeople(tx); err != nil {
		return personRef{}, false, err
	}
	return personRef{ID: target, Name: name}, merged, nil
}

// deletePerson removes a name from the list and from every album.
func (s *store) deletePerson(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM album_people WHERE person_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM people WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errPersonNotFound
	}
	return s.commitPeople(tx)
}

// deleteAlbumPeople removes every entry of the given albums.
func (s *store) deleteAlbumPeople(albums []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, album := range albums {
		if _, err := tx.Exec(`DELETE FROM album_people WHERE album = ?`, album); err != nil {
			return err
		}
	}
	return s.commitPeople(tx)
}

func (s *store) commitPeople(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cache.peopleRevision.Add(1)
	return nil
}

// withModels makes an album without models answer [] instead of null.
func withModels(p albumPeople) albumPeople {
	if p.Models == nil {
		p.Models = []personRef{}
	}
	return p
}

// organizeAPI serves the organize page: the author and model lists and the
// people of each album. Writes need the session and a same-origin request.
type organizeAPI struct {
	store *store
}

func (a *organizeAPI) routes(mux *http.ServeMux, gate *authGate) {
	mux.Handle("GET /api/people", gate.protect(http.HandlerFunc(a.people)))
	mux.Handle("PUT /api/album-people", gate.protect(http.HandlerFunc(a.assign)))
	mux.Handle("DELETE /api/album-people/stale", gate.protect(http.HandlerFunc(a.cleanStale)))
	mux.Handle("PUT /api/people/{id}", gate.protect(http.HandlerFunc(a.rename)))
	mux.Handle("DELETE /api/people/{id}", gate.protect(http.HandlerFunc(a.remove)))
}

type personView struct {
	personRef
	Albums int `json:"albums"`
}

type peopleView struct {
	Authors []personView `json:"authors"`
	Models  []personView `json:"models"`
	// Stale counts albums whose folder is gone while their people remain.
	Stale int `json:"stale"`
}

// currentAlbums returns the ids of the albums that have photos now.
func (a *organizeAPI) currentAlbums() (map[string]bool, error) {
	index, err := a.store.photoIndex()
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool)
	for album := range index.albums {
		ids[album] = true
	}
	return ids, nil
}

// view lists both name lists with the number of current albums using each.
func (a *organizeAPI) view(current map[string]bool) (peopleView, error) {
	people, err := a.store.listPeople()
	if err != nil {
		return peopleView{}, err
	}
	links, err := a.store.albumPeopleMap()
	if err != nil {
		return peopleView{}, err
	}
	v := peopleView{Authors: []personView{}, Models: []personView{}}
	uses := make(map[int64]int)
	for album, ap := range links {
		if !current[album] {
			v.Stale++
			continue
		}
		if ap.Author != nil {
			uses[ap.Author.ID]++
		}
		for _, m := range ap.Models {
			uses[m.ID]++
		}
	}
	for _, p := range people {
		pv := personView{personRef: p.personRef, Albums: uses[p.ID]}
		if p.role == roleAuthor {
			v.Authors = append(v.Authors, pv)
		} else {
			v.Models = append(v.Models, pv)
		}
	}
	return v, nil
}

// respond answers a request with the refreshed name lists added.
func (a *organizeAPI) respond(w http.ResponseWriter, body map[string]any) {
	current, err := a.currentAlbums()
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	v, err := a.view(current)
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	body["people"] = v
	writeJSON(w, http.StatusOK, body)
}

func (a *organizeAPI) people(w http.ResponseWriter, r *http.Request) {
	current, err := a.currentAlbums()
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	v, err := a.view(current)
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type albumPeopleInput struct {
	Albums []string  `json:"albums"`
	Author *string   `json:"author"`
	Models *[]string `json:"models"`
}

// assign sets the author and/or the models of one or more current albums.
func (a *organizeAPI) assign(w http.ResponseWriter, r *http.Request) {
	if !sameOriginJSON(w, r, organizeOriginError) {
		return
	}
	var in albumPeopleInput
	if !decodeOrganize(w, r, &in, 1<<20) {
		return
	}
	if in.Author == nil && in.Models == nil {
		writeOrganizeError(w, invalid("没有要修改的作者或模特"))
		return
	}
	if len(in.Albums) == 0 || len(in.Albums) > maxAlbumsPerRequest {
		writeOrganizeError(w, invalid("一次可以修改 1–%d 本相册", maxAlbumsPerRequest))
		return
	}
	current, err := a.currentAlbums()
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	albums := make([]string, 0, len(in.Albums))
	seen := make(map[string]bool)
	for _, raw := range in.Albums {
		id, ok := validAlbumID(raw)
		if !ok || !current[id] {
			writeOrganizeError(w, invalid("相册不存在或已改名，请刷新页面"))
			return
		}
		if !seen[id] {
			seen[id] = true
			albums = append(albums, id)
		}
	}
	var author *string
	if in.Author != nil {
		name := ""
		if strings.TrimSpace(*in.Author) != "" {
			if name, err = cleanPersonName(*in.Author); err != nil {
				writeOrganizeError(w, err)
				return
			}
		}
		author = &name
	}
	var models []string
	if in.Models != nil {
		models = []string{}
		keys := make(map[string]bool)
		for _, raw := range *in.Models {
			name, err := cleanPersonName(raw)
			if err != nil {
				writeOrganizeError(w, err)
				return
			}
			if key := nameKey(name); !keys[key] {
				keys[key] = true
				models = append(models, name)
			}
		}
		if len(models) > maxModelsPerAlbum {
			writeOrganizeError(w, invalid("每本相册最多 %d 位模特", maxModelsPerAlbum))
			return
		}
	}
	if err := a.store.setAlbumPeople(albums, author, models); err != nil {
		writeOrganizeError(w, err)
		return
	}
	links, err := a.store.albumPeopleMap()
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	type item struct {
		ID string `json:"id"`
		albumPeople
	}
	out := make([]item, 0, len(albums))
	for _, id := range albums {
		out = append(out, item{ID: id, albumPeople: withModels(links[id])})
	}
	slog.Info("album people saved", "albums", len(albums), "author", author != nil, "models", models != nil)
	a.respond(w, map[string]any{"albums": out})
}

// rename renames a person, merging into an existing name of the same role.
func (a *organizeAPI) rename(w http.ResponseWriter, r *http.Request) {
	if !sameOriginJSON(w, r, organizeOriginError) {
		return
	}
	id, ok := personIDFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decodeOrganize(w, r, &in, 4<<10) {
		return
	}
	name, err := cleanPersonName(in.Name)
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	ref, merged, err := a.store.renamePerson(id, name)
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	slog.Info("person renamed", "id", id, "into", ref.ID, "merged", merged)
	a.respond(w, map[string]any{"person": ref, "merged": merged})
}

// remove deletes a person from the list and from every album; photos stay.
func (a *organizeAPI) remove(w http.ResponseWriter, r *http.Request) {
	if !sameOriginOK(w, r, organizeOriginError) {
		return
	}
	id, ok := personIDFromPath(w, r)
	if !ok {
		return
	}
	if err := a.store.deletePerson(id); err != nil {
		writeOrganizeError(w, err)
		return
	}
	slog.Info("person deleted", "id", id)
	a.respond(w, map[string]any{"ok": true})
}

// cleanStale drops the people of albums whose folder no longer exists.
func (a *organizeAPI) cleanStale(w http.ResponseWriter, r *http.Request) {
	if !sameOriginOK(w, r, organizeOriginError) {
		return
	}
	current, err := a.currentAlbums()
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	links, err := a.store.albumPeopleMap()
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	var stale []string
	for album := range links {
		if !current[album] {
			stale = append(stale, album)
		}
	}
	if err := a.store.deleteAlbumPeople(stale); err != nil {
		writeOrganizeError(w, err)
		return
	}
	slog.Info("stale album people removed", "albums", len(stale))
	a.respond(w, map[string]any{"removed": len(stale)})
}

func decodeOrganize(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式不正确"})
		return false
	}
	return true
}

func personIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeOrganizeError(w, errPersonNotFound)
		return 0, false
	}
	return id, true
}

func writeOrganizeError(w http.ResponseWriter, err error) {
	var ue *userError
	switch {
	case errors.As(err, &ue):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": ue.msg})
	case errors.Is(err, errPersonNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "找不到这个名字，请刷新页面"})
	default:
		slog.Error("organize", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "保存失败，请查看容器日志"})
	}
}
