package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeService records the requests it gets and answers each with the next
// handler, the last one repeating.
type fakeService struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	requests []fakeRequest
	answers  []func(w http.ResponseWriter, body map[string]any)
}

type fakeRequest struct {
	path   string
	header http.Header
	body   map[string]any
}

func newFakeService(t *testing.T, answers ...func(w http.ResponseWriter, body map[string]any)) *fakeService {
	f := &fakeService{t: t, answers: answers}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		n := len(f.requests)
		f.requests = append(f.requests, fakeRequest{path: r.URL.Path, header: r.Header.Clone(), body: body})
		answer := f.answers[min(n, len(f.answers)-1)]
		f.mu.Unlock()
		answer(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeService) seen() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRequest(nil), f.requests...)
}

func chatAnswer(content string) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, _ map[string]any) {
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
	}
}

func statusAnswer(code int, body string) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

// recognizeHarness has albums needing people, one filled album and examples:
//
//	[LE] LEDG-100 G.su           nothing filled
//	Min_E                         nothing filled
//	Jia/Pure Media Vol.1 Jia      model Jia, no author
//	LEEHEE EXPRESS - LERB-018 G.su  filled (example)
//	filled                        filled (example)
//	root.jpg                      the root album, never recognized
func recognizeHarness(t *testing.T) *settingsHarness {
	t.Helper()
	h := newSettingsHarness(t)
	for i, rel := range []string{"[LE] LEDG-100 G.su/1.jpg", "Min_E/1.jpg", "Jia/Pure Media Vol.1 Jia/1.jpg", "LEEHEE EXPRESS - LERB-018 G.su/1.jpg", "filled/1.jpg", "root.jpg"} {
		if _, err := h.st.upsert(photo{RelPath: rel, Size: 10, MtimeUnix: int64(1_700_000_000 + i), Width: 40, Height: 30}); err != nil {
			t.Fatal(err)
		}
	}
	assignNow(t, h.st, []string{"LEEHEE EXPRESS - LERB-018 G.su"}, strp("LEEHEE EXPRESS"), []string{"G.su"})
	assignNow(t, h.st, []string{"filled"}, strp("Loozy"), []string{"Min.E"})
	assignNow(t, h.st, []string{"Jia/Pure Media Vol.1 Jia"}, nil, []string{"Jia"})
	return h
}

func configureRecognizer(t *testing.T, h *settingsHarness, c map[string]any) {
	t.Helper()
	if code, out, raw := h.json(t, "PUT", "/api/recognizer", c); code != http.StatusOK {
		t.Fatalf("save recognizer = %d %v %s", code, out, raw)
	}
}

func peopleSnapshot(t *testing.T, st *store) string {
	t.Helper()
	links, err := st.loadAlbumPeople()
	if err != nil {
		t.Fatal(err)
	}
	people, err := st.listPeople()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]any{links, people})
	return string(raw)
}

func TestRecognizerNormalize(t *testing.T) {
	good := recognizerConfig{Provider: "custom", Endpoint: " https://api.example.com/v1/ ", APIKey: " sk-1 ", Model: " deepseek-chat ", Reasoning: "high"}
	c, err := good.normalize()
	if err != nil || c.Endpoint != "https://api.example.com/v1" || c.APIKey != "sk-1" || c.Model != "deepseek-chat" {
		t.Fatalf("normalize = %+v, %v", c, err)
	}
	// Fixed services never keep an address.
	if c, err := (recognizerConfig{Provider: "gemini", Endpoint: "https://evil.example", APIKey: "k", Model: "m"}).normalize(); err != nil || c.Endpoint != "" {
		t.Fatalf("fixed service kept %q, %v", c.Endpoint, err)
	}
	for name, bad := range map[string]recognizerConfig{
		"no provider":      {APIKey: "k", Model: "m"},
		"unknown provider": {Provider: "llama", APIKey: "k", Model: "m"},
		"custom no url":    {Provider: "custom", APIKey: "k", Model: "m"},
		"custom ftp":       {Provider: "custom", Endpoint: "ftp://x/v1", APIKey: "k", Model: "m"},
		"custom query":     {Provider: "custom", Endpoint: "https://x/v1?key=1", APIKey: "k", Model: "m"},
		"custom user":      {Provider: "custom", Endpoint: "https://u:p@x/v1", APIKey: "k", Model: "m"},
		"no model":         {Provider: "openai", APIKey: "k"},
		"model space":      {Provider: "openai", APIKey: "k", Model: "gpt 5"},
		"long model":       {Provider: "openai", APIKey: "k", Model: strings.Repeat("m", 101)},
		"no key":           {Provider: "openai", Model: "m"},
		"key newline":      {Provider: "openai", APIKey: "a\nb", Model: "m"},
		"bad reasoning":    {Provider: "openai", APIKey: "k", Model: "m", Reasoning: "xhigh"},
	} {
		if _, err := bad.normalize(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestRecognizerAPIGatesKeyAndConfig(t *testing.T) {
	h := recognizeHarness(t)
	routes := [][2]string{{"GET", "/api/recognizer"}, {"PUT", "/api/recognizer"}, {"DELETE", "/api/recognizer"}, {"POST", "/api/recognizer/test"}, {"POST", "/api/recognize"}}
	for _, r := range routes {
		if code, _ := h.do(t, r[0], r[1], "application/json", "{}", false); code != http.StatusUnauthorized {
			t.Fatalf("%s %s without login = %d", r[0], r[1], code)
		}
	}
	for _, r := range routes[1:] {
		code, out := h.send(t, r[0], r[1], map[string]any{}, map[string]string{"Origin": "https://evil.example"})
		if code != http.StatusForbidden || out["error"] != recognizerOriginError {
			t.Fatalf("cross-site %s %s = %d %v", r[0], r[1], code, out)
		}
	}
	if code, _ := h.do(t, "PUT", "/api/recognizer", "text/plain", `{"provider":"openai"}`, true); code != http.StatusUnsupportedMediaType {
		t.Fatalf("plain text = %d", code)
	}
	if code, out, _ := h.json(t, "GET", "/api/recognizer", nil); code != http.StatusOK || out["configured"] != false {
		t.Fatalf("empty config = %d %v", code, out)
	}
	if code, out, _ := h.json(t, "PUT", "/api/recognizer", map[string]any{"provider": "openai", "model": "m"}); code != http.StatusBadRequest || out["error"] != "请填写 API Key" {
		t.Fatalf("first save without key = %d %v", code, out)
	}
	if code, out, _ := h.json(t, "PUT", "/api/recognizer", map[string]any{"provider": "openai", "apiKey": "k", "model": "m", "extra": 1}); code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %v", code, out)
	}

	code, _, raw := h.json(t, "PUT", "/api/recognizer", map[string]any{"provider": "openai", "apiKey": "sk-secret-1", "model": "gpt-test", "reasoning": "low"})
	if code != http.StatusOK || strings.Contains(raw, "sk-secret-1") {
		t.Fatalf("save = %d %s", code, raw)
	}
	_, out, raw := h.json(t, "GET", "/api/recognizer", nil)
	want := map[string]any{"configured": true, "provider": "openai", "endpoint": "", "model": "gpt-test", "reasoning": "low", "hasKey": true}
	if !reflect.DeepEqual(out, want) || strings.Contains(raw, "sk-secret-1") {
		t.Fatalf("view = %v %s", out, raw)
	}
	// The settings page gets the same view with everything else.
	if _, out, raw := h.json(t, "GET", "/api/settings", nil); !reflect.DeepEqual(out["recognizer"], want) || strings.Contains(raw, "sk-secret-1") {
		t.Fatalf("settings recognizer = %v", out["recognizer"])
	}
	// An empty key keeps the saved one for the same service...
	configureRecognizer(t, h, map[string]any{"provider": "openai", "apiKey": "", "model": "gpt-other", "reasoning": ""})
	if c, _, _ := h.st.getRecognizer(); c.APIKey != "sk-secret-1" || c.Model != "gpt-other" || c.Reasoning != "" {
		t.Fatalf("kept key = %+v", c)
	}
	// ...but never goes to another company.
	if code, out, _ := h.json(t, "PUT", "/api/recognizer", map[string]any{"provider": "grok", "apiKey": "", "model": "grok-4.7"}); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "换了识别服务") {
		t.Fatalf("switch without key = %d %v", code, out)
	}
	if code, out, _ := h.json(t, "POST", "/api/recognizer/test", map[string]any{"provider": "grok", "apiKey": "", "model": "grok-4.7"}); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "换了识别服务") {
		t.Fatalf("test another service without key = %d %v", code, out)
	}
	if c, _, _ := h.st.getRecognizer(); c.Provider != "openai" {
		t.Fatalf("refused switch changed config: %+v", c)
	}
	if code, _, _ := h.json(t, "DELETE", "/api/recognizer", nil); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if _, out, _ := h.json(t, "GET", "/api/recognizer", nil); out["configured"] != false {
		t.Fatalf("after delete = %v", out)
	}
	if code, out, _ := h.json(t, "POST", "/api/recognize", map[string]any{"albums": []string{"Min_E"}}); code != http.StatusConflict || !strings.Contains(out["error"].(string), "设置页") {
		t.Fatalf("recognize unconfigured = %d %v", code, out)
	}
}

func TestRecognizeOpenAIFormatChecksEverySuggestion(t *testing.T) {
	h := recognizeHarness(t)
	answer := `{"albums":[
	  {"album":"[LE] LEDG-100 G.su","author":"leehee express","models":["G.SU","New  Girl"]},
	  {"album":"Min_E","author":"","models":["Min.E","min.e"]},
	  {"album":"Jia/Pure Media Vol.1 Jia","author":"Pure Media","models":["Jia"]},
	  {"album":"filled","author":"Someone","models":[]},
	  {"album":"nope","author":"Ghost","models":[]},
	  {"album":"Min_E","author":"Twice","models":[]}
	]}`
	svc := newFakeService(t, chatAnswer(answer))
	configureRecognizer(t, h, map[string]any{"provider": "custom", "endpoint": svc.srv.URL + "/v1/", "apiKey": "sk-test", "model": "fake-model", "reasoning": "high"})
	before := peopleSnapshot(t, h.st)
	code, out, raw := h.json(t, "POST", "/api/recognize", map[string]any{"albums": []string{"[LE] LEDG-100 G.su", "Min_E", "Jia/Pure Media Vol.1 Jia", "filled", ".", "missing", "Min_E"}})
	if code != http.StatusOK {
		t.Fatalf("recognize = %d %s", code, raw)
	}

	req := svc.seen()
	if len(req) != 1 {
		t.Fatalf("requests = %d", len(req))
	}
	r := req[0]
	if r.path != "/v1/chat/completions" || r.header.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("request to %s with %q", r.path, r.header.Get("Authorization"))
	}
	if r.body["model"] != "fake-model" || r.body["reasoning_effort"] != "high" {
		t.Fatalf("body = %v", r.body)
	}
	for _, absent := range []string{"temperature", "max_tokens", "max_completion_tokens", "top_p"} {
		if _, ok := r.body[absent]; ok {
			t.Fatalf("sent %s", absent)
		}
	}
	format, _ := r.body["response_format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Fatalf("response_format = %v", r.body["response_format"])
	}
	messages := r.body["messages"].([]any)
	system := messages[0].(map[string]any)
	if system["role"] != "system" || !strings.Contains(system["content"].(string), "never the photographer") {
		t.Fatalf("system = %v", system)
	}
	var prompt recognitionPrompt
	if err := json.Unmarshal([]byte(messages[1].(map[string]any)["content"].(string)), &prompt); err != nil {
		t.Fatal(err)
	}
	var asked []string
	for _, a := range prompt.Albums {
		asked = append(asked, a.Album+"="+strings.Join(a.Need, "+"))
	}
	if strings.Join(asked, " | ") != "[LE] LEDG-100 G.su=author+models | Min_E=author+models | Jia/Pure Media Vol.1 Jia=author" {
		t.Fatalf("asked %v", asked)
	}
	if prompt.Albums[2].Known == nil || !reflect.DeepEqual(prompt.Albums[2].Known.Models, []string{"Jia"}) {
		t.Fatalf("known = %+v", prompt.Albums[2].Known)
	}
	var examples []string
	for _, e := range prompt.Examples {
		examples = append(examples, e.Album+"="+e.Author+"/"+strings.Join(e.Models, ","))
	}
	if strings.Join(examples, " | ") != "LEEHEE EXPRESS - LERB-018 G.su=LEEHEE EXPRESS/G.su | filled=Loozy/Min.E" {
		t.Fatalf("examples %v", examples)
	}
	if !reflect.DeepEqual(prompt.Roster.Authors, []string{"LEEHEE EXPRESS", "Loozy"}) || !reflect.DeepEqual(prompt.Roster.Models, []string{"G.su", "Jia", "Min.E"}) {
		t.Fatalf("roster %+v", prompt.Roster)
	}

	var got struct {
		Recognized  int               `json:"recognized"`
		Skipped     int               `json:"skipped"`
		Suggestions []albumSuggestion `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	want := []albumSuggestion{
		{Album: "[LE] LEDG-100 G.su", Author: &suggestedName{Name: "LEEHEE EXPRESS"}, Models: []suggestedName{{Name: "G.su"}, {Name: "New Girl", New: true}}},
		{Album: "Min_E", Models: []suggestedName{{Name: "Min.E"}}},
		{Album: "Jia/Pure Media Vol.1 Jia", Author: &suggestedName{Name: "Pure Media", New: true}},
	}
	// Skipped: Jia's models (already filled), "filled", "nope", Min_E twice.
	if got.Recognized != 3 || got.Skipped != 4 || !reflect.DeepEqual(got.Suggestions, want) {
		t.Fatalf("got %+v %s", got, raw)
	}
	_ = out
	if after := peopleSnapshot(t, h.st); after != before {
		t.Fatalf("recognize wrote album people:\n%s\n%s", before, after)
	}
}

func TestRecognizeRetriesWithoutSchemaAndReadsFencedJSON(t *testing.T) {
	h := recognizeHarness(t)
	svc := newFakeService(t,
		statusAnswer(http.StatusBadRequest, `{"error":{"message":"response_format type json_schema is unavailable"}}`),
		chatAnswer("好的：\n```json\n{\"albums\":[{\"album\":\"Min_E\",\"author\":\"\",\"models\":[\"Min.E\"]}]}\n```"),
	)
	configureRecognizer(t, h, map[string]any{"provider": "custom", "endpoint": svc.srv.URL, "apiKey": "k", "model": "m"})
	code, out, raw := h.json(t, "POST", "/api/recognize", map[string]any{"albums": []string{"Min_E"}})
	if code != http.StatusOK || !strings.Contains(raw, `"name":"Min.E"`) {
		t.Fatalf("recognize = %d %v", code, out)
	}
	req := svc.seen()
	if len(req) != 2 {
		t.Fatalf("requests = %d", len(req))
	}
	if _, ok := req[0].body["response_format"]; !ok {
		t.Fatal("first request had no schema")
	}
	if _, ok := req[1].body["response_format"]; ok {
		t.Fatal("retry still sent the schema")
	}
	// Default reasoning sends nothing.
	if _, ok := req[0].body["reasoning_effort"]; ok {
		t.Fatal("default reasoning was sent")
	}
}

func TestRecognizeReportsServiceFailures(t *testing.T) {
	h := recognizeHarness(t)
	before := peopleSnapshot(t, h.st)
	for _, c := range []struct {
		name   string
		answer func(http.ResponseWriter, map[string]any)
		code   int
		want   string
	}{
		{"bad key", statusAnswer(401, `{"error":{"message":"Incorrect API key provided"}}`), 502, "API Key 无效，请在设置页检查（服务返回：Incorrect API key provided）"},
		{"no model", statusAnswer(404, `[{"error":{"code":404,"message":"models/x is not found"}}]`), 502, "找不到这个模型或接口地址，请检查模型名和地址（服务返回：models/x is not found）"},
		{"limited", statusAnswer(429, `{"error":"quota exceeded"}`), 502, "限流或额度不足"},
		{"down", statusAnswer(503, `<html>busy</html>`), 502, "HTTP 503"},
		{"not json", chatAnswer("I think it is Min.E."), 502, "不是要求的 JSON 格式"},
		{"no albums", chatAnswer(`{"result":[]}`), 502, "不是要求的 JSON 格式"},
		{"refused", func(w http.ResponseWriter, _ map[string]any) {
			writeJSON(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": nil, "refusal": "I can't help"}, "finish_reason": "stop"}}})
		}, 502, "拒绝回答"},
		{"cut off", func(w http.ResponseWriter, _ map[string]any) {
			writeJSON(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"albums":[`}, "finish_reason": "length"}}})
		}, 502, "截断"},
		{"empty", chatAnswer("  "), 502, "没有给出回答"},
	} {
		svc := newFakeService(t, c.answer)
		configureRecognizer(t, h, map[string]any{"provider": "custom", "endpoint": svc.srv.URL, "apiKey": "k", "model": "m"})
		code, out, _ := h.json(t, "POST", "/api/recognize", map[string]any{"albums": []string{"Min_E"}})
		if code != c.code || !strings.Contains(out["error"].(string), c.want) {
			t.Fatalf("%s = %d %v", c.name, code, out)
		}
	}
	// A service that never answers ends at the batch deadline.
	recognizeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { recognizeTimeout = 50 * time.Second })
	release := make(chan struct{})
	slow := newFakeService(t, func(w http.ResponseWriter, _ map[string]any) { <-release })
	t.Cleanup(func() { close(release) })
	configureRecognizer(t, h, map[string]any{"provider": "custom", "endpoint": slow.srv.URL, "apiKey": "k", "model": "m"})
	start := time.Now()
	code, out, _ := h.json(t, "POST", "/api/recognize", map[string]any{"albums": []string{"Min_E"}})
	if code != http.StatusGatewayTimeout || !strings.Contains(out["error"].(string), "没有回答") || time.Since(start) > 5*time.Second {
		t.Fatalf("slow = %d %v after %v", code, out, time.Since(start))
	}
	// Unreachable services name the network.
	configureRecognizer(t, h, map[string]any{"provider": "custom", "endpoint": "http://127.0.0.1:1", "apiKey": "k", "model": "m"})
	if code, out, _ := h.json(t, "POST", "/api/recognize", map[string]any{"albums": []string{"Min_E"}}); code != http.StatusBadGateway || !strings.Contains(out["error"].(string), "连不上识别服务") {
		t.Fatalf("unreachable = %d %v", code, out)
	}
	for _, body := range []map[string]any{{"albums": []string{}}, {"albums": make([]string, 21)}, {"album": "x"}} {
		if code, out, _ := h.json(t, "POST", "/api/recognize", body); code != http.StatusBadRequest {
			t.Fatalf("bad request %v = %d %v", body, code, out)
		}
	}
	// Albums that need nothing never reach the service.
	idle := newFakeService(t, chatAnswer(`{"albums":[]}`))
	configureRecognizer(t, h, map[string]any{"provider": "custom", "endpoint": idle.srv.URL, "apiKey": "k", "model": "m"})
	if code, out, _ := h.json(t, "POST", "/api/recognize", map[string]any{"albums": []string{"filled", ".", "missing"}}); code != http.StatusOK || out["recognized"] != float64(0) || len(idle.seen()) != 0 {
		t.Fatalf("nothing to do = %d %v, %d requests", code, out, len(idle.seen()))
	}
	if after := peopleSnapshot(t, h.st); after != before {
		t.Fatal("failures changed album people")
	}
}

func TestRecognizeClaudeUsesMessagesAPI(t *testing.T) {
	h := recognizeHarness(t)
	message := func(stop, text string) func(http.ResponseWriter, map[string]any) {
		return func(w http.ResponseWriter, _ map[string]any) {
			writeJSON(w, 200, map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-test",
				"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": stop, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}})
		}
	}
	svc := newFakeService(t,
		message("end_turn", `{"albums":[{"album":"Min_E","author":"","models":["MIN.E"]}]}`),
		message("refusal", ""),
		statusAnswer(401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`),
	)
	claudeEndpoint = svc.srv.URL
	t.Cleanup(func() { claudeEndpoint = "https://api.anthropic.com" })
	configureRecognizer(t, h, map[string]any{"provider": "claude", "apiKey": "sk-ant-test", "model": "claude-test", "reasoning": "low"})
	code, _, raw := h.json(t, "POST", "/api/recognize", map[string]any{"albums": []string{"Min_E"}})
	if code != http.StatusOK || !strings.Contains(raw, `{"album":"Min_E","models":[{"name":"Min.E","new":false}]}`) {
		t.Fatalf("claude = %d %s", code, raw)
	}
	r := svc.seen()[0]
	if r.path != "/v1/messages" || r.header.Get("X-Api-Key") != "sk-ant-test" || r.header.Get("Anthropic-Version") == "" {
		t.Fatalf("claude request %s %v", r.path, r.header)
	}
	if r.body["model"] != "claude-test" || r.body["max_tokens"] != float64(16000) {
		t.Fatalf("claude body %v", r.body)
	}
	for _, absent := range []string{"temperature", "thinking", "top_p"} {
		if _, ok := r.body[absent]; ok {
			t.Fatalf("claude sent %s", absent)
		}
	}
	config := r.body["output_config"].(map[string]any)
	format := config["format"].(map[string]any)
	if config["effort"] != "low" || format["type"] != "json_schema" || format["schema"] == nil {
		t.Fatalf("output_config %v", config)
	}
	if system := r.body["system"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(system, "never the photographer") {
		t.Fatalf("system %q", system)
	}
	if code, out, _ := h.json(t, "POST", "/api/recognize", map[string]any{"albums": []string{"Min_E"}}); code != 502 || !strings.Contains(out["error"].(string), "拒绝回答") {
		t.Fatalf("refusal = %d %v", code, out)
	}
	if code, out, _ := h.json(t, "POST", "/api/recognize", map[string]any{"albums": []string{"Min_E"}}); code != 502 || out["error"] != "API Key 无效，请在设置页检查（服务返回：invalid x-api-key）" {
		t.Fatalf("bad key = %d %v", code, out)
	}
	// Default reasoning leaves effort out.
	configureRecognizer(t, h, map[string]any{"provider": "claude", "apiKey": "", "model": "claude-test", "reasoning": ""})
	h.json(t, "POST", "/api/recognize", map[string]any{"albums": []string{"Min_E"}})
	last := svc.seen()[len(svc.seen())-1]
	if _, ok := last.body["output_config"].(map[string]any)["effort"]; ok {
		t.Fatalf("default reasoning sent effort: %v", last.body["output_config"])
	}
}

func TestRecognizerTestDoesNotSave(t *testing.T) {
	h := recognizeHarness(t)
	svc := newFakeService(t, chatAnswer(`{"albums":[{"album":"Studio Example - Vol.1 Model Example","author":"Studio Example","models":["Model Example"]}]}`), chatAnswer("nope"))
	form := map[string]any{"provider": "custom", "endpoint": svc.srv.URL, "apiKey": "sk-form", "model": "m", "reasoning": "medium"}
	if code, out, _ := h.json(t, "POST", "/api/recognizer/test", form); code != http.StatusOK || out["ok"] != true {
		t.Fatalf("test = %d %v", code, out)
	}
	if r := svc.seen()[0]; r.header.Get("Authorization") != "Bearer sk-form" || r.body["reasoning_effort"] != "medium" {
		t.Fatalf("test request %v %v", r.header, r.body)
	}
	if _, ok, _ := h.st.getRecognizer(); ok {
		t.Fatal("test saved the config")
	}
	if code, out, _ := h.json(t, "POST", "/api/recognizer/test", form); code != http.StatusBadGateway || !strings.Contains(out["error"].(string), "JSON") {
		t.Fatalf("bad format test = %d %v", code, out)
	}
	delete(form, "apiKey")
	if code, out, _ := h.json(t, "POST", "/api/recognizer/test", form); code != http.StatusBadRequest || out["error"] != "请填写 API Key" {
		t.Fatalf("test without key = %d %v", code, out)
	}
}

// A custom service at another address is another company: the saved key stays
// home unless the owner types one for the new address.
func TestRecognizerKeepsKeyOnlyAtTheSameAddress(t *testing.T) {
	h := recognizeHarness(t)
	first := newFakeService(t, chatAnswer(`{"albums":[]}`))
	second := newFakeService(t, chatAnswer(`{"albums":[]}`))
	configureRecognizer(t, h, map[string]any{"provider": "custom", "endpoint": first.srv.URL + "/v1", "apiKey": "sk-first", "model": "m"})

	sameAddress := map[string]any{"provider": "custom", "endpoint": " " + first.srv.URL + "/v1/ ", "apiKey": "", "model": "m"}
	if code, out, _ := h.json(t, "POST", "/api/recognizer/test", sameAddress); code != http.StatusOK || out["ok"] != true {
		t.Fatalf("same address without key = %d %v", code, out)
	}
	if seen := first.seen(); len(seen) != 1 || seen[0].header.Get("Authorization") != "Bearer sk-first" {
		t.Fatalf("same address request %v", seen)
	}

	moved := map[string]any{"provider": "custom", "endpoint": second.srv.URL + "/v1", "apiKey": "", "model": "m"}
	for _, method := range []string{"POST", "PUT"} {
		path := map[string]string{"POST": "/api/recognizer/test", "PUT": "/api/recognizer"}[method]
		if code, out, _ := h.json(t, method, path, moved); code != http.StatusBadRequest || out["error"] != "换了接口地址，请重新填写 API Key" {
			t.Fatalf("%s another address without key = %d %v", method, code, out)
		}
	}
	if seen := second.seen(); len(seen) != 0 {
		t.Fatalf("the saved key reached another address: %v", seen)
	}
	if c, _, _ := h.st.getRecognizer(); c.Endpoint != first.srv.URL+"/v1" || c.APIKey != "sk-first" {
		t.Fatalf("refused move changed config: %+v", c)
	}

	moved["apiKey"] = "sk-second"
	configureRecognizer(t, h, moved)
	if c, _, _ := h.st.getRecognizer(); c.Endpoint != second.srv.URL+"/v1" || c.APIKey != "sk-second" {
		t.Fatalf("new address with its own key = %+v", c)
	}
}

func TestParseRecognition(t *testing.T) {
	for _, text := range []string{
		`{"albums":[]}`,
		"```json\n{\"albums\":[{\"album\":\"a\",\"author\":\"x\",\"models\":[]}]}\n```",
		`Here you go: {"albums":[{"album":"a","author":"x","models":["y"]}]} Hope it helps.`,
	} {
		if _, err := parseRecognition(text); err != nil {
			t.Fatalf("rejected %q", text)
		}
	}
	for _, text := range []string{"", "no json", `{"albums":{}}`, `{"albums":null}`, `{"other":[]}`, `{"albums":[1,2]}`, `{"albums":[`} {
		if _, err := parseRecognition(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}
