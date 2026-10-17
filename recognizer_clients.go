package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Requests to the recognition service. Claude goes through its own Messages
// API, where the JSON format and reasoning level are honored; every other
// service uses the OpenAI chat format. Neither sends temperature: newer Claude
// models reject values below 1, and the default suits extraction.

const recognitionSystem = `You suggest the author and the models of photo albums from their folder paths.
- An album is identified by its folder path relative to the photo library. Parent folders count too: "Azuki/Aug 2022" is an album of the model Azuki.
- The author is the studio, label, brand or publisher that released the photo set, for example LEEHEE EXPRESS, Pure Media or Loozy. It is never the photographer, and never a reposting site, watermark or channel such as MrCong.com, 4kUp or a Telegram channel. An album has at most one author.
- The models are the people who appear in the photos, one entry per person, at most 10.
- The roster lists the names already in use. When a folder refers to someone on the roster, through another spelling, romanization, word order, an abbreviation such as "[LE]" or a nickname, answer with the roster spelling exactly. Answer a new name only when nobody on the roster matches.
- The examples are albums the owner has already filled in. Follow their conventions.
- Fill only the fields listed in "need" for each album; "known" shows what the album already has. When you are not confident, leave the author as "" or the models as []. Never turn a title word, a series name, a product code or a date into a name.
- Answer with JSON only, in exactly this shape, one entry per album in the request: {"albums":[{"album":"<the path as given>","author":"<name or empty>","models":["<name>"]}]}`

// recognitionSchema is the answer format for services that enforce one.
var recognitionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"albums": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"album":  map[string]any{"type": "string"},
					"author": map[string]any{"type": "string"},
					"models": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required":             []string{"album", "author", "models"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"albums"},
	"additionalProperties": false,
}

// serviceError is a failure the recognition service reported, worded for the
// owner, with a short excerpt of what the service said.
type serviceError struct {
	message string
	detail  string
	timeout bool
	// formatRefused marks a 400 about the JSON schema, worth one retry without it.
	formatRefused bool
}

func (e *serviceError) Error() string {
	if e.detail == "" {
		return e.message
	}
	return e.message + "（服务返回：" + e.detail + "）"
}

func (e *serviceError) status() int {
	if e.timeout {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// ask sends the prompt and returns the text of the answer.
func (a *recognizerAPI) ask(ctx context.Context, c recognizerConfig, p recognitionPrompt) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	if c.Provider == providerClaude {
		return askClaude(ctx, c, string(raw))
	}
	base := c.Endpoint
	if c.Provider != providerCustom {
		base = recognizerEndpoints[c.Provider]
	}
	return a.askOpenAIFormat(ctx, c, base, string(raw))
}

func askClaude(ctx context.Context, c recognizerConfig, prompt string) (string, error) {
	// No retries: the owner sees the reason and can press 识别 again, and a
	// retry would eat into the batch's 50 seconds.
	client := anthropic.NewClient(option.WithAPIKey(c.APIKey), option.WithBaseURL(claudeEndpoint), option.WithMaxRetries(0))
	config := anthropic.OutputConfigParam{Format: anthropic.JSONOutputFormatParam{Schema: recognitionSchema}}
	if c.Reasoning != "" {
		config.Effort = anthropic.OutputConfigEffort(c.Reasoning)
	}
	message, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:        anthropic.Model(c.Model),
		MaxTokens:    16000,
		System:       []anthropic.TextBlockParam{{Text: recognitionSystem}},
		Messages:     []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))},
		OutputConfig: config,
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return "", statusError(apiErr.StatusCode, apiErr.RawJSON())
		}
		return "", transportError(ctx, err)
	}
	switch message.StopReason {
	case anthropic.StopReasonRefusal:
		return "", &serviceError{message: "识别服务拒绝回答这次请求"}
	case anthropic.StopReasonMaxTokens:
		return "", &serviceError{message: "识别服务的回答太长被截断，请减少相册后再试"}
	}
	var text strings.Builder
	for _, block := range message.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", &serviceError{message: "识别服务没有给出回答"}
	}
	return text.String(), nil
}

func (a *recognizerAPI) askOpenAIFormat(ctx context.Context, c recognizerConfig, base, prompt string) (string, error) {
	body := map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": recognitionSystem},
			{"role": "user", "content": prompt},
		},
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "album_people", "strict": true, "schema": recognitionSchema},
		},
	}
	if c.Reasoning != "" {
		body["reasoning_effort"] = c.Reasoning
	}
	text, err := a.postChat(ctx, c, base, body)
	// Some compatible services take no JSON schema. Ask once more without it;
	// the instructions still ask for the same JSON.
	var se *serviceError
	if errors.As(err, &se) && se.formatRefused {
		delete(body, "response_format")
		text, err = a.postChat(ctx, c, base, body)
	}
	return text, err
}

func (a *recognizerAPI) postChat(ctx context.Context, c recognizerConfig, base string, body map[string]any) (string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", &serviceError{message: "接口地址无法使用，请检查设置"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	res, err := a.client.Do(req)
	if err != nil {
		return "", transportError(ctx, err)
	}
	defer res.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", transportError(ctx, err)
	}
	if res.StatusCode != http.StatusOK {
		e := statusError(res.StatusCode, string(answer))
		lower := strings.ToLower(string(answer))
		if _, sent := body["response_format"]; sent && res.StatusCode == http.StatusBadRequest && (strings.Contains(lower, "response_format") || strings.Contains(lower, "json_schema")) {
			e.formatRefused = true
		}
		return "", e
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
				Refusal string          `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(answer, &out); err != nil || len(out.Choices) == 0 {
		return "", &serviceError{message: "识别服务的回答无法读取，请检查接口地址是否兼容 OpenAI 格式"}
	}
	choice := out.Choices[0]
	switch {
	case choice.Message.Refusal != "":
		return "", &serviceError{message: "识别服务拒绝回答这次请求", detail: excerpt(choice.Message.Refusal)}
	case choice.FinishReason == "length":
		return "", &serviceError{message: "识别服务的回答太长被截断，请减少相册后再试"}
	}
	text := chatContent(choice.Message.Content)
	if strings.TrimSpace(text) == "" {
		return "", &serviceError{message: "识别服务没有给出回答"}
	}
	return text, nil
}

// chatContent reads a message content given as a string or as text parts.
func chatContent(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Type == "text" || part.Type == "" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func statusError(code int, body string) *serviceError {
	e := &serviceError{detail: excerpt(serviceMessage(body))}
	switch {
	case code == http.StatusUnauthorized:
		e.message = "API Key 无效，请在设置页检查"
	case code == http.StatusForbidden:
		e.message = "这个 API Key 没有权限使用该模型"
	case code == http.StatusPaymentRequired:
		e.message = "识别服务额度不足"
	case code == http.StatusNotFound:
		e.message = "找不到这个模型或接口地址，请检查模型名和地址"
	case code == http.StatusTooManyRequests:
		e.message = "识别服务限流或额度不足，请稍后再试"
	case code == http.StatusBadRequest || code == http.StatusUnprocessableEntity:
		e.message = "识别服务拒绝了请求，多半是模型名或推理强度不被支持"
	case code >= 500:
		e.message = fmt.Sprintf("识别服务暂时不可用（HTTP %d），请稍后再试", code)
	default:
		e.message = fmt.Sprintf("识别服务返回了错误（HTTP %d）", code)
	}
	return e
}

func transportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &serviceError{message: fmt.Sprintf("识别服务 %d 秒内没有回答，可以减少相册或调低推理强度后再试", int(recognizeTimeout.Seconds())), timeout: true}
	}
	if ctx.Err() != nil {
		return &serviceError{message: "识别已取消"}
	}
	return &serviceError{message: "连不上识别服务，请检查接口地址和网络", detail: excerpt(err.Error())}
}

// serviceMessage pulls the error message out of the usual JSON envelopes:
// {"error":{"message":…}}, {"error":"…"}, [{"error":{…}}] or {"message":…}.
func serviceMessage(body string) string {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "[") {
		var list []json.RawMessage
		if json.Unmarshal([]byte(trimmed), &list) == nil && len(list) > 0 {
			trimmed = string(list[0])
		}
	}
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal([]byte(trimmed), &envelope) != nil {
		return trimmed
	}
	var inner struct {
		Message string `json:"message"`
	}
	var plain string
	switch {
	case json.Unmarshal(envelope.Error, &inner) == nil && inner.Message != "":
		return inner.Message
	case json.Unmarshal(envelope.Error, &plain) == nil && plain != "":
		return plain
	case envelope.Message != "":
		return envelope.Message
	}
	return trimmed
}

func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= 200 {
		return s
	}
	return string([]rune(s)[:200]) + "…"
}
