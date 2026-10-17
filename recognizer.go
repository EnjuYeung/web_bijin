package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The recognition service is an outside AI that reads album folder paths and
// suggests the author and models of albums that have none. It is called only
// when the owner presses 识别 or 测试; suggestions are never saved here, the
// organize page saves the ones the owner accepts.

const (
	providerClaude = "claude"
	providerOpenAI = "openai"
	providerGemini = "gemini"
	providerGrok   = "grok"
	providerCustom = "custom"

	maxRecognizeAlbums   = 20
	maxRecognizeExamples = 300
	maxRecognizerModel   = 100
	maxRecognizerKey     = 1000
	recognizerOriginError = "只能从相册网站使用识别服务"
)

// recognizeTimeout bounds one batch: OpenResty cuts a request at its default
// 60 s read timeout. Tests shorten it.
var recognizeTimeout = 50 * time.Second

// recognizerEndpoints are the OpenAI-format bases of the fixed services.
var recognizerEndpoints = map[string]string{
	providerOpenAI: "https://api.openai.com/v1",
	providerGemini: "https://generativelanguage.googleapis.com/v1beta/openai",
	providerGrok:   "https://api.x.ai/v1",
}

// claudeEndpoint is the Messages API base. Tests point it at a fake server.
var claudeEndpoint = "https://api.anthropic.com"

var reasoningLevels = map[string]bool{"": true, "low": true, "medium": true, "high": true}

type recognizerConfig struct {
	Provider  string `json:"provider"`
	Endpoint  string `json:"endpoint"`
	APIKey    string `json:"apiKey"`
	Model     string `json:"model"`
	Reasoning string `json:"reasoning"`
}

// recognizerView is the saved configuration without the key.
type recognizerView struct {
	Configured bool   `json:"configured"`
	Provider   string `json:"provider"`
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	Reasoning  string `json:"reasoning"`
	HasKey     bool   `json:"hasKey"`
}

func (c recognizerConfig) view() recognizerView {
	return recognizerView{Configured: true, Provider: c.Provider, Endpoint: c.Endpoint, Model: c.Model, Reasoning: c.Reasoning, HasKey: c.APIKey != ""}
}

// normalize checks what the settings page sent. Only custom services keep an
// address; the others use their fixed one.
func (c recognizerConfig) normalize() (recognizerConfig, error) {
	c.Provider = strings.TrimSpace(c.Provider)
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Model = strings.TrimSpace(c.Model)
	c.Reasoning = strings.TrimSpace(c.Reasoning)
	switch c.Provider {
	case providerClaude, providerOpenAI, providerGemini, providerGrok:
		c.Endpoint = ""
	case providerCustom:
		u, err := url.Parse(c.Endpoint)
		if c.Endpoint == "" {
			return c, invalid("请填写接口地址")
		}
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, invalid("接口地址要以 http:// 或 https:// 开头，不能带 ? 或 #，例如 https://api.example.com/v1")
		}
		c.Endpoint = strings.TrimRight(u.String(), "/")
	default:
		return c, invalid("请选择识别服务")
	}
	switch {
	case c.Model == "":
		return c, invalid("请填写模型名")
	case utf8.RuneCountInString(c.Model) > maxRecognizerModel:
		return c, invalid("模型名最多 %d 个字", maxRecognizerModel)
	case strings.IndexFunc(c.Model, unicode.IsSpace) >= 0 || strings.IndexFunc(c.Model, unicode.IsControl) >= 0:
		return c, invalid("模型名不能包含空格")
	}
	switch {
	case c.APIKey == "":
		return c, invalid("请填写 API Key")
	case len(c.APIKey) > maxRecognizerKey:
		return c, invalid("API Key 太长了，请检查是否粘贴了多余内容")
	case strings.IndexFunc(c.APIKey, unicode.IsSpace) >= 0 || strings.IndexFunc(c.APIKey, unicode.IsControl) >= 0:
		return c, invalid("API Key 不能包含空格或换行")
	}
	if !reasoningLevels[c.Reasoning] {
		return c, invalid("推理强度只能是默认、低、中或高")
	}
	return c, nil
}

func (s *store) migrateRecognizer() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS recognizer (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  provider TEXT NOT NULL,
  endpoint TEXT NOT NULL DEFAULT '',
  api_key TEXT NOT NULL,
  model TEXT NOT NULL,
  reasoning TEXT NOT NULL DEFAULT ''
)`)
	return err
}

func (s *store) getRecognizer() (recognizerConfig, bool, error) {
	var c recognizerConfig
	err := s.db.QueryRow(`SELECT provider, endpoint, api_key, model, reasoning FROM recognizer WHERE id = 1`).Scan(&c.Provider, &c.Endpoint, &c.APIKey, &c.Model, &c.Reasoning)
	if err == sql.ErrNoRows {
		return c, false, nil
	}
	return c, err == nil, err
}

func (s *store) saveRecognizer(c recognizerConfig) error {
	_, err := s.db.Exec(`INSERT INTO recognizer (id, provider, endpoint, api_key, model, reasoning) VALUES (1, ?, ?, ?, ?, ?)
	  ON CONFLICT(id) DO UPDATE SET provider = excluded.provider, endpoint = excluded.endpoint, api_key = excluded.api_key, model = excluded.model, reasoning = excluded.reasoning`,
		c.Provider, c.Endpoint, c.APIKey, c.Model, c.Reasoning)
	return err
}

func (s *store) deleteRecognizer() error {
	_, err := s.db.Exec(`DELETE FROM recognizer WHERE id = 1`)
	return err
}

type recognizerAPI struct {
	store *store
	// client sends the OpenAI-format requests; Claude uses its SDK's own.
	client *http.Client
}

func newRecognizerAPI(st *store) *recognizerAPI {
	return &recognizerAPI{store: st, client: &http.Client{}}
}

func (a *recognizerAPI) routes(mux *http.ServeMux, gate *authGate) {
	mux.Handle("GET /api/recognizer", gate.protect(http.HandlerFunc(a.get)))
	mux.Handle("PUT /api/recognizer", gate.protectWrite(recognizerOriginError, true, a.put))
	mux.Handle("DELETE /api/recognizer", gate.protectWrite(recognizerOriginError, false, a.remove))
	mux.Handle("POST /api/recognizer/test", gate.protectWrite(recognizerOriginError, true, a.test))
	mux.Handle("POST /api/recognize", gate.protectWrite(recognizerOriginError, true, a.recognize))
}

func (a *recognizerAPI) get(w http.ResponseWriter, r *http.Request) {
	c, ok, err := a.store.getRecognizer()
	if err != nil {
		writeRecognizerError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !ok {
		writeJSON(w, http.StatusOK, recognizerView{})
		return
	}
	writeJSON(w, http.StatusOK, c.view())
}

// input reads the form. An empty key keeps the saved one, but only for the
// same service: a key never travels to another company's API.
func (a *recognizerAPI) input(w http.ResponseWriter, r *http.Request) (recognizerConfig, bool) {
	var in recognizerConfig
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式不正确"})
		return in, false
	}
	if strings.TrimSpace(in.APIKey) == "" {
		saved, ok, err := a.store.getRecognizer()
		if err != nil {
			writeRecognizerError(w, err)
			return in, false
		}
		if ok && saved.Provider == strings.TrimSpace(in.Provider) {
			in.APIKey = saved.APIKey
		} else if ok && strings.TrimSpace(in.Provider) != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "换了识别服务，请填写这家服务的 API Key"})
			return in, false
		}
	}
	c, err := in.normalize()
	if err != nil {
		writeRecognizerError(w, err)
		return c, false
	}
	return c, true
}

func (a *recognizerAPI) put(w http.ResponseWriter, r *http.Request) {
	c, ok := a.input(w, r)
	if !ok {
		return
	}
	if err := a.store.saveRecognizer(c); err != nil {
		writeRecognizerError(w, err)
		return
	}
	slog.Info("recognizer saved", "provider", c.Provider, "model", c.Model, "reasoning", c.Reasoning)
	writeJSON(w, http.StatusOK, c.view())
}

func (a *recognizerAPI) remove(w http.ResponseWriter, r *http.Request) {
	if err := a.store.deleteRecognizer(); err != nil {
		writeRecognizerError(w, err)
		return
	}
	slog.Info("recognizer deleted")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// test asks the service about one made-up album in the real answer format,
// so a wrong key, model, reasoning level or format support shows up here.
func (a *recognizerAPI) test(w http.ResponseWriter, r *http.Request) {
	c, ok := a.input(w, r)
	if !ok {
		return
	}
	prompt := recognitionPrompt{Albums: []promptAlbum{{Album: "Studio Example - Vol.1 Model Example", Need: []string{"author", "models"}}}}
	start := time.Now()
	ctx, cancel := contextWithTimeout(r, recognizeTimeout)
	defer cancel()
	text, err := a.ask(ctx, c, prompt)
	if err == nil {
		_, err = parseRecognition(text)
	}
	slog.Info("recognizer test", "provider", c.Provider, "model", c.Model, "ms", time.Since(start).Milliseconds(), "ok", err == nil)
	if err != nil {
		writeRecognizerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ms": time.Since(start).Milliseconds()})
}

type recognizeRequest struct {
	Albums []string `json:"albums"`
}

type suggestedName struct {
	Name string `json:"name"`
	New  bool   `json:"new"`
}

type albumSuggestion struct {
	Album  string          `json:"album"`
	Author *suggestedName  `json:"author,omitempty"`
	Models []suggestedName `json:"models,omitempty"`
}

func (a *recognizerAPI) recognize(w http.ResponseWriter, r *http.Request) {
	var in recognizeRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式不正确"})
		return
	}
	if len(in.Albums) == 0 || len(in.Albums) > maxRecognizeAlbums {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "每次识别 1–20 本相册"})
		return
	}
	c, ok, err := a.store.getRecognizer()
	if err != nil {
		writeRecognizerError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "还没有配置识别服务，请先在设置页配置"})
		return
	}
	prompt, err := a.store.recognitionPrompt(in.Albums)
	if err != nil {
		writeRecognizerError(w, err)
		return
	}
	if len(prompt.Albums) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"recognized": 0, "suggestions": []albumSuggestion{}, "skipped": 0})
		return
	}
	start := time.Now()
	ctx, cancel := contextWithTimeout(r, recognizeTimeout)
	defer cancel()
	text, err := a.ask(ctx, c, prompt)
	var suggestions []albumSuggestion
	skipped := 0
	if err == nil {
		var answer recognitionAnswer
		answer, err = parseRecognition(text)
		if err == nil {
			suggestions, skipped = checkRecognition(answer, prompt)
		}
	}
	slog.Info("recognize", "provider", c.Provider, "model", c.Model, "albums", len(prompt.Albums), "ms", time.Since(start).Milliseconds(), "suggested", len(suggestions), "skipped", skipped, "ok", err == nil)
	if err != nil {
		writeRecognizerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recognized": len(prompt.Albums), "suggestions": suggestions, "skipped": skipped})
}

// promptAlbum is one album to recognize: what it lacks and what it has.
type promptAlbum struct {
	Album  string       `json:"album"`
	Need   []string     `json:"need"`
	Known  *knownPeople `json:"known,omitempty"`
	author bool
	models bool
}

type knownPeople struct {
	Author string   `json:"author,omitempty"`
	Models []string `json:"models,omitempty"`
}

type exampleAlbum struct {
	Album  string   `json:"album"`
	Author string   `json:"author"`
	Models []string `json:"models"`
}

type rosterNames struct {
	Authors []string `json:"authors"`
	Models  []string `json:"models"`
}

type recognitionPrompt struct {
	Roster   rosterNames    `json:"roster"`
	Examples []exampleAlbum `json:"examples"`
	Albums   []promptAlbum  `json:"albums"`
}

// recognitionPrompt reads the current albums and people. Albums that are gone,
// already filled in or the root (its name says nothing) are left out.
func (s *store) recognitionPrompt(requested []string) (recognitionPrompt, error) {
	var p recognitionPrompt
	index, err := s.photoIndex()
	if err != nil {
		return p, err
	}
	links, err := s.albumPeopleMap()
	if err != nil {
		return p, err
	}
	people, err := s.listPeople()
	if err != nil {
		return p, err
	}
	p.Roster = rosterNames{Authors: []string{}, Models: []string{}}
	for _, person := range people {
		if person.role == roleAuthor {
			p.Roster.Authors = append(p.Roster.Authors, person.Name)
		} else {
			p.Roster.Models = append(p.Roster.Models, person.Name)
		}
	}
	// wanted holds the albums asked about; a requested album that is already
	// filled in stays an example.
	wanted := map[string]bool{}
	for _, raw := range requested {
		album, ok := validAlbumID(raw)
		if !ok || album == rootAlbumID || wanted[album] || len(index.albums[album]) == 0 {
			continue
		}
		linked := links[album]
		target := promptAlbum{Album: album, author: linked.Author == nil, models: len(linked.Models) == 0}
		if !target.author && !target.models {
			continue
		}
		wanted[album] = true
		known := knownPeople{}
		if target.author {
			target.Need = append(target.Need, "author")
		} else {
			known.Author = linked.Author.Name
		}
		if target.models {
			target.Need = append(target.Need, "models")
		} else {
			for _, m := range linked.Models {
				known.Models = append(known.Models, m.Name)
			}
		}
		if known.Author != "" || len(known.Models) > 0 {
			target.Known = &known
		}
		p.Albums = append(p.Albums, target)
	}
	albums := make([]string, 0, len(links))
	for album := range links {
		if !wanted[album] && len(index.albums[album]) > 0 {
			albums = append(albums, album)
		}
	}
	sort.Strings(albums)
	p.Examples = []exampleAlbum{}
	for _, album := range albums {
		if len(p.Examples) == maxRecognizeExamples {
			break
		}
		linked := links[album]
		example := exampleAlbum{Album: album, Models: []string{}}
		if linked.Author != nil {
			example.Author = linked.Author.Name
		}
		for _, m := range linked.Models {
			example.Models = append(example.Models, m.Name)
		}
		p.Examples = append(p.Examples, example)
	}
	return p, nil
}

type recognitionAnswer struct {
	Albums []struct {
		Album  string   `json:"album"`
		Author string   `json:"author"`
		Models []string `json:"models"`
	} `json:"albums"`
}

var errBadAnswer = errors.New("bad recognition answer")

// parseRecognition reads the answer JSON, tolerating a code fence or words
// around it from services that ignore the requested format.
func parseRecognition(text string) (recognitionAnswer, error) {
	var answer recognitionAnswer
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return answer, errBadAnswer
	}
	var raw struct {
		Albums json.RawMessage `json:"albums"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &raw); err != nil || len(raw.Albums) == 0 || raw.Albums[0] != '[' {
		return answer, errBadAnswer
	}
	if err := json.Unmarshal(raw.Albums, &answer.Albums); err != nil {
		return answer, errBadAnswer
	}
	return answer, nil
}

// checkRecognition keeps the suggestions that fill an empty field of an album
// in this batch with a valid name. A name already on the list takes the list's
// spelling. Everything else is dropped and counted.
func checkRecognition(answer recognitionAnswer, p recognitionPrompt) ([]albumSuggestion, int) {
	targets := make(map[string]promptAlbum, len(p.Albums))
	for _, album := range p.Albums {
		targets[album.Album] = album
	}
	roster := map[string]map[string]string{roleAuthor: {}, roleModel: {}}
	for _, name := range p.Roster.Authors {
		roster[roleAuthor][nameKey(name)] = name
	}
	for _, name := range p.Roster.Models {
		roster[roleModel][nameKey(name)] = name
	}
	named := func(role, raw string) (suggestedName, bool) {
		name, err := cleanPersonName(raw)
		if err != nil {
			return suggestedName{}, false
		}
		if listed, ok := roster[role][nameKey(name)]; ok {
			return suggestedName{Name: listed}, true
		}
		return suggestedName{Name: name, New: true}, true
	}
	seen := map[string]bool{}
	skipped := 0
	out := []albumSuggestion{}
	for _, item := range answer.Albums {
		author := strings.TrimSpace(item.Author)
		var models []string
		for _, m := range item.Models {
			if strings.TrimSpace(m) != "" {
				models = append(models, m)
			}
		}
		if author == "" && len(models) == 0 {
			continue
		}
		target, ok := targets[item.Album]
		if !ok || seen[item.Album] {
			skipped++
			continue
		}
		seen[item.Album] = true
		suggestion := albumSuggestion{Album: item.Album}
		if author != "" {
			if name, ok := named(roleAuthor, author); ok && target.author {
				suggestion.Author = &name
			} else {
				skipped++
			}
		}
		if len(models) > 0 && !target.models {
			skipped++
			models = nil
		}
		keys := map[string]bool{}
		for _, raw := range models {
			name, ok := named(roleModel, raw)
			switch {
			case !ok || len(suggestion.Models) == maxModelsPerAlbum:
				skipped++
			case !keys[nameKey(name.Name)]:
				keys[nameKey(name.Name)] = true
				suggestion.Models = append(suggestion.Models, name)
			}
		}
		if suggestion.Author != nil || len(suggestion.Models) > 0 {
			out = append(out, suggestion)
		}
	}
	return out, skipped
}

func writeRecognizerError(w http.ResponseWriter, err error) {
	var ue *userError
	var se *serviceError
	switch {
	case errors.As(err, &ue):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": ue.msg})
	case errors.As(err, &se):
		writeJSON(w, se.status(), map[string]any{"error": se.Error()})
	case errors.Is(err, errBadAnswer):
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "识别服务的回答不是要求的 JSON 格式，可以换个模型再试"})
	default:
		slog.Error("recognizer", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "识别失败，请查看容器日志"})
	}
}
