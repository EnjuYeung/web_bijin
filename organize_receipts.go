package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Receipts are confirmation facts, never a durable execution queue. The floor
// prevents a pruned receipt's ID from being executed again, even before TTL.
const (
	organizeReceiptTTL   = 48 * time.Hour
	organizeReceiptLimit = 256
	organizeReceiptBytes = 16 << 20
)

var errOrganizeReceiptExpired = errors.New("保存回执已过期，无法确认这次修改；请先核对相册")

var organizeIDPattern = regexp.MustCompile(`^([0-9]{13})-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type organizeChange struct {
	OperationID string    `json:"operationId"`
	Kind        string    `json:"kind"`
	Albums      []string  `json:"albums,omitempty"`
	Author      *string   `json:"author,omitempty"`
	Models      *[]string `json:"models,omitempty"`
	PersonID    int64     `json:"personId,omitempty"`
	Name        string    `json:"name,omitempty"`
}

type organizeAlbum struct {
	ID string `json:"id"`
	albumPeople
}

type organizeResult struct {
	OperationID string          `json:"operationId"`
	Albums      []organizeAlbum `json:"albums"`
	People      peopleView      `json:"people"`
	Person      *personRef      `json:"person,omitempty"`
	Merged      bool            `json:"merged,omitempty"`
	Removed     int             `json:"removed,omitempty"`
}

func organizeIssued(id string) (int64, error) {
	match := organizeIDPattern.FindStringSubmatch(id)
	if match == nil {
		return 0, invalid("保存编号不正确，请重新打开整理页")
	}
	issued, err := strconv.ParseInt(match[1], 10, 64)
	return issued, err
}

func receiptExpired(tx *sql.Tx, issued int64, now time.Time) (bool, error) {
	var floor int64
	if err := tx.QueryRow(`SELECT before_ms FROM organize_receipt_floor WHERE id = 1`).Scan(&floor); err != nil {
		return false, err
	}
	return issued <= floor || issued <= now.Add(-organizeReceiptTTL).UnixMilli(), nil
}

func (a *organizeAPI) change(w http.ResponseWriter, r *http.Request) {
	if !sameOriginJSON(w, r, organizeOriginError) {
		return
	}
	var in organizeChange
	if !decodeOrganize(w, r, &in, 1<<20) {
		return
	}
	current, err := a.currentAlbums()
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	result, err := a.store.applyOrganize(in, current, time.Now())
	if errors.Is(err, errOrganizeReceiptExpired) {
		writeJSON(w, http.StatusGone, map[string]any{"error": err.Error()})
		return
	}
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

func (a *organizeAPI) receipt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("operation")
	issued, err := organizeIssued(id)
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	defer tx.Rollback()
	var result []byte
	err = tx.QueryRow(`SELECT result FROM organize_receipts WHERE operation_id = ?`, id).Scan(&result)
	w.Header().Set("Cache-Control", "no-store")
	if err == nil {
		// Release the single SQLite connection before sending a potentially
		// large receipt to a slow or disconnected browser.
		_ = tx.Rollback()
		writeJSON(w, http.StatusOK, map[string]any{"state": "committed", "result": json.RawMessage(result)})
		return
	}
	if err != sql.ErrNoRows {
		writeOrganizeError(w, err)
		return
	}
	expired, err := receiptExpired(tx, issued, time.Now())
	if err != nil {
		writeOrganizeError(w, err)
		return
	}
	state := "missing"
	if expired {
		state = "expired"
	}
	_ = tx.Rollback()
	writeJSON(w, http.StatusOK, map[string]any{"state": state})
}

func (s *store) applyOrganize(in organizeChange, current map[string]bool, now time.Time) ([]byte, error) {
	issued, err := organizeIssued(in.OperationID)
	if err != nil {
		return nil, err
	}
	if issued > now.Add(5*time.Minute).UnixMilli() {
		return nil, invalid("设备时间不正确，请校准后重新打开整理页")
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(hash[:])
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var previousHash string
	var previous []byte
	err = tx.QueryRow(`SELECT fingerprint, result FROM organize_receipts WHERE operation_id = ?`, in.OperationID).Scan(&previousHash, &previous)
	if err == nil {
		if previousHash != fingerprint {
			return nil, invalid("保存编号对应的内容不能改变")
		}
		return previous, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	expired, err := receiptExpired(tx, issued, now)
	if err != nil {
		return nil, err
	}
	if expired {
		return nil, errOrganizeReceiptExpired
	}
	out := organizeResult{OperationID: in.OperationID}
	switch in.Kind {
	case "assign":
		albums, author, models, err := normalizeAlbumAssignment(albumPeopleInput{Albums: in.Albums, Author: in.Author, Models: in.Models}, current)
		if err != nil {
			return nil, err
		}
		if err := setAlbumPeopleTx(tx, albums, author, models); err != nil {
			return nil, err
		}
	case "rename":
		name, err := cleanPersonName(in.Name)
		if err != nil {
			return nil, err
		}
		ref, merged, err := renamePersonTx(tx, in.PersonID, name)
		if err != nil {
			return nil, err
		}
		out.Person, out.Merged = &ref, merged
	case "remove":
		if err := deletePersonTx(tx, in.PersonID); err != nil {
			return nil, err
		}
	case "cleanStale":
		links, err := loadAlbumPeopleFrom(tx)
		if err != nil {
			return nil, err
		}
		var stale []string
		for album := range links {
			if !current[album] {
				stale = append(stale, album)
			}
		}
		if err := deleteAlbumPeopleTx(tx, stale); err != nil {
			return nil, err
		}
		out.Removed = len(stale)
	default:
		return nil, invalid("没有这个整理操作")
	}
	// Build the answer before committing: a read failure rolls back the change,
	// and a lost HTTP answer can be retrieved verbatim from the same transaction.
	links, err := loadAlbumPeopleFrom(tx)
	if err != nil {
		return nil, err
	}
	people, err := listPeopleFrom(tx)
	if err != nil {
		return nil, err
	}
	out.People = peopleViewOf(people, links, current)
	ids := make([]string, 0, len(current))
	for id := range current {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out.Albums = make([]organizeAlbum, 0, len(ids))
	for _, id := range ids {
		out.Albums = append(out.Albums, organizeAlbum{ID: id, albumPeople: withModels(links[id])})
	}
	result, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO organize_receipts (operation_id, issued_ms, fingerprint, result) VALUES (?, ?, ?, ?)`, in.OperationID, issued, fingerprint, result); err != nil {
		return nil, err
	}
	if err := pruneOrganizeReceipts(tx, now); err != nil {
		return nil, err
	}
	if err := s.commitPeople(tx); err != nil {
		return nil, err
	}
	return result, nil
}

func pruneOrganizeReceipts(tx *sql.Tx, now time.Time) error {
	cutoff := now.Add(-organizeReceiptTTL).UnixMilli()
	rows, err := tx.Query(`SELECT issued_ms, length(result) FROM organize_receipts ORDER BY issued_ms DESC, operation_id DESC`)
	if err != nil {
		return err
	}
	count, size := 0, 0
	for rows.Next() {
		var issued int64
		var bytes int
		if err := rows.Scan(&issued, &bytes); err != nil {
			rows.Close()
			return err
		}
		count++
		size += bytes
		if count > organizeReceiptLimit || size > organizeReceiptBytes {
			if issued > cutoff {
				cutoff = issued
			}
			break
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM organize_receipts WHERE issued_ms <= ?`, cutoff); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE organize_receipt_floor SET before_ms = MAX(before_ms, ?) WHERE id = 1`, cutoff)
	return err
}

func normalizeAlbumAssignment(in albumPeopleInput, current map[string]bool) ([]string, *string, []string, error) {
	if in.Author == nil && in.Models == nil {
		return nil, nil, nil, invalid("没有要修改的作者或模特")
	}
	if len(in.Albums) == 0 || len(in.Albums) > maxAlbumsPerRequest {
		return nil, nil, nil, invalid("一次可以修改 1–%d 本相册", maxAlbumsPerRequest)
	}
	albums := make([]string, 0, len(in.Albums))
	seen := make(map[string]bool)
	for _, raw := range in.Albums {
		id, ok := validAlbumID(raw)
		if !ok || !current[id] {
			return nil, nil, nil, invalid("相册不存在或已改名，请刷新页面")
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
			var err error
			name, err = cleanPersonName(*in.Author)
			if err != nil {
				return nil, nil, nil, err
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
				return nil, nil, nil, err
			}
			if key := nameKey(name); !keys[key] {
				keys[key] = true
				models = append(models, name)
			}
		}
		if len(models) > maxModelsPerAlbum {
			return nil, nil, nil, invalid("每本相册最多 %d 位模特", maxModelsPerAlbum)
		}
	}
	return albums, author, models, nil
}

func peopleViewOf(people []personRow, links map[string]albumPeople, current map[string]bool) peopleView {
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
	return v
}
