package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, body string) *Request {
	t.Helper()
	req, err := ParseRequest([]byte(body), false)
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	return req
}

// 转成 JSON 再比较，避免 json.Number 和 float64 的差别。
func asJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParseRequestBasics(t *testing.T) {
	req := mustParse(t, `{
		"model": "claude-sonnet-5-5", "max_tokens": 1024, "stream": true,
		"system": [
			{"type": "text", "text": "x-anthropic-billing-header: cc_version=1.0; cch=abc"},
			{"type": "text", "text": "You are helpful.", "cache_control": {"type": "ephemeral"}}
		],
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": [{"type": "text", "text": "hello"}]},
			{"role": "user", "content": [{"type": "text", "text": "what's 2+2?"}]}
		],
		"temperature": 0.5, "top_p": 0.9, "top_k": 40, "stop_sequences": ["END"]
	}`)
	if req.Model != "claude-sonnet-5-5" || !req.Stream {
		t.Fatalf("model/stream = %q %v", req.Model, req.Stream)
	}
	// 计费块被去掉，只留真正的 system 文本。
	if got := asJSON(t, req.Body["systemInstruction"]); got != `{"parts":[{"text":"You are helpful."}],"role":"user"}` {
		t.Fatalf("systemInstruction = %s", got)
	}
	want := `[{"parts":[{"text":"hi"}],"role":"user"},{"parts":[{"text":"hello"}],"role":"model"},{"parts":[{"text":"what's 2+2?"}],"role":"user"}]`
	if got := asJSON(t, req.Body["contents"]); got != want {
		t.Fatalf("contents = %s", got)
	}
	if got := asJSON(t, req.Body["generationConfig"]); got != `{"maxOutputTokens":1024,"stopSequences":["END"],"temperature":0.5,"topK":40,"topP":0.9}` {
		t.Fatalf("generationConfig = %s", got)
	}
}

func TestParseRequestToolsAndResults(t *testing.T) {
	req := mustParse(t, `{
		"model": "m", "max_tokens": 100,
		"tools": [
			{"name": "get_weather", "description": "Weather", "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}}},
			{"type": "web_search_20250305", "name": "web_search", "max_uses": 3}
		],
		"tool_choice": {"type": "tool", "name": "get_weather"},
		"messages": [
			{"role": "user", "content": "weather?"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "Paris", "n": 12345678901234567890}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "text", "text": "sunny"}, {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "AAAA"}}]},
				{"type": "tool_result", "tool_use_id": "toolu_x", "content": "boom", "is_error": true},
				{"type": "text", "text": "thanks"}
			]}
		]
	}`)
	if got := asJSON(t, req.Body["tools"]); got != `[{"functionDeclarations":[{"description":"Weather","name":"get_weather","parameters":{"properties":{"city":{"type":"string"}},"type":"object"}}]}]` {
		t.Fatalf("tools = %s", got)
	}
	if got := asJSON(t, req.Body["toolConfig"]); got != `{"functionCallingConfig":{"allowedFunctionNames":["get_weather"],"mode":"ANY"}}` {
		t.Fatalf("toolConfig = %s", got)
	}
	if !reflect.DeepEqual(req.Dropped, []string{"server tool web_search_20250305"}) {
		t.Fatalf("Dropped = %v", req.Dropped)
	}
	contents := req.Body["contents"].([]any)
	// 大整数不丢精度。
	if got := asJSON(t, contents[1]); got != `{"parts":[{"functionCall":{"args":{"city":"Paris","n":12345678901234567890},"id":"toolu_1","name":"get_weather"}}],"role":"model"}` {
		t.Fatalf("assistant = %s", got)
	}
	want := `{"parts":[` +
		`{"functionResponse":{"id":"toolu_1","name":"get_weather","parts":[{"inlineData":{"data":"AAAA","mimeType":"image/png"}}],"response":{"result":"sunny"}}},` +
		`{"functionResponse":{"id":"toolu_x","name":"toolu_x","response":{"error":"boom"}}},` +
		`{"text":"thanks"}],"role":"user"}`
	if got := asJSON(t, contents[2]); got != want {
		t.Fatalf("user = %s", got)
	}
}

// 思考块：带文字和签名的转成思考 part；只带签名的（Gemini）挂到后面的工具调用上；不带签名的丢掉；
// 用户消息里的思考块忽略。
func TestParseRequestThinking(t *testing.T) {
	req := mustParse(t, `{
		"model": "m", "max_tokens": 100,
		"thinking": {"type": "enabled", "budget_tokens": 8000},
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "let me think", "signature": "sigC"},
				{"type": "thinking", "thinking": "", "signature": "sigG"},
				{"type": "tool_use", "id": "t1", "name": "f", "input": {}},
				{"type": "thinking", "thinking": "no sig"},
				{"type": "redacted_thinking", "data": "xxx"},
				{"type": "text", "text": "done"}
			]},
			{"role": "user", "content": [{"type": "thinking", "thinking": "user side", "signature": "s"}, {"type": "tool_result", "tool_use_id": "t1", "content": "ok"}]}
		]
	}`)
	contents := req.Body["contents"].([]any)
	want := `{"parts":[{"text":"let me think","thought":true,"thoughtSignature":"sigC"},{"functionCall":{"args":{},"id":"t1","name":"f"},"thoughtSignature":"sigG"},{"text":"done"}],"role":"model"}`
	if got := asJSON(t, contents[1]); got != want {
		t.Fatalf("assistant = %s", got)
	}
	if got := asJSON(t, contents[2]); got != `{"parts":[{"functionResponse":{"id":"t1","name":"f","response":{"result":"ok"}}}],"role":"user"}` {
		t.Fatalf("user = %s", got)
	}
	if got := asJSON(t, req.Body["generationConfig"]); got != `{"maxOutputTokens":100,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":8000}}` {
		t.Fatalf("generationConfig = %s", got)
	}
	if !reflect.DeepEqual(req.Dropped, []string{"unsigned thinking block", "redacted_thinking block"}) {
		t.Fatalf("Dropped = %v", req.Dropped)
	}
}

func TestParseRequestThinkingModes(t *testing.T) {
	cases := map[string]string{
		`"thinking": {"type": "adaptive"}`:                                     `{"includeThoughts":true,"thinkingLevel":"high"}`,
		`"thinking": {"type": "adaptive"}, "output_config": {"effort": "low"}`: `{"includeThoughts":true,"thinkingLevel":"low"}`,
		`"thinking": {"type": "adaptive"}, "output_config": {"effort": "max"}`: `{"includeThoughts":true,"thinkingLevel":"xhigh"}`,
		`"output_config": {"effort": "medium"}`:                                `{"thinkingLevel":"medium"}`,
		`"thinking": {"type": "disabled"}`:                                     `null`,
	}
	for extra, want := range cases {
		req := mustParse(t, `{"model": "m", "max_tokens": 10, "messages": [{"role": "user", "content": "x"}], `+extra+`}`)
		gen := req.Body["generationConfig"].(map[string]any)
		if got := asJSON(t, gen["thinkingConfig"]); got != want {
			t.Errorf("%s: thinkingConfig = %s，应为 %s", extra, got, want)
		}
	}
}

// 对话中间的 system、developer 消息转成用户消息里的 <system-reminder>，和相邻的用户消息合并；计费块照样去掉。
func TestParseRequestInlineSystem(t *testing.T) {
	req := mustParse(t, `{"model": "m", "max_tokens": 10, "messages": [
		{"role": "user", "content": "hi"},
		{"role": "system", "content": [{"type": "text", "text": "x-anthropic-billing-header: cc=1"}, {"type": "text", "text": "Be careful."}]},
		{"role": "assistant", "content": "ok"},
		{"role": "developer", "content": "Note this."},
		{"role": "user", "content": "go on"}
	]}`)
	want := `[{"parts":[{"text":"hi"},{"text":"<system-reminder>\nBe careful.\n</system-reminder>"}],"role":"user"},` +
		`{"parts":[{"text":"ok"}],"role":"model"},` +
		`{"parts":[{"text":"<system-reminder>\nNote this.\n</system-reminder>"},{"text":"go on"}],"role":"user"}]`
	// 用不转义 < > 的编码，便于对照。
	got := jsonNoEscape(t, req.Body["contents"])
	if got != want {
		t.Fatalf("contents =\n%s\nwant\n%s", got, want)
	}
}

func TestParseRequestSources(t *testing.T) {
	req := mustParse(t, `{"model": "m", "max_tokens": 10, "messages": [{"role": "user", "content": [
		{"type": "image", "source": {"type": "url", "url": "https://example.com/a.png"}},
		{"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "JVBE"}},
		{"type": "document", "source": {"type": "text", "media_type": "text/plain", "data": "plain doc"}}
	]}]}`)
	want := `[{"parts":[{"fileData":{"fileUri":"https://example.com/a.png","mimeType":"image/*"}},{"inlineData":{"data":"JVBE","mimeType":"application/pdf"}},{"text":"plain doc"}],"role":"user"}]`
	if got := asJSON(t, req.Body["contents"]); got != want {
		t.Fatalf("contents = %s", got)
	}
}

func TestParseRequestInvalid(t *testing.T) {
	cases := map[string]string{
		`not json`: "invalid JSON",
		`{"max_tokens": 1, "messages": [{"role": "user", "content": "x"}]}`:                                                                           "model",
		`{"model": "m", "messages": [{"role": "user", "content": "x"}]}`:                                                                              "max_tokens",
		`{"model": "m", "max_tokens": 1, "messages": []}`:                                                                                             "messages",
		`{"model": "m", "max_tokens": 1, "messages": [{"role": "tool", "content": "x"}]}`:                                                             "role",
		`{"model": "m", "max_tokens": 1, "messages": [{"role": "user", "content": [{"type": "image", "source": {"type": "file", "file_id": "f"}}]}]}`: "Files API",
	}
	for body, want := range cases {
		_, err := ParseRequest([]byte(body), false)
		var ie *InvalidError
		if !errors.As(err, &ie) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v，应包含 %q", body, err, want)
		}
	}
	// count_tokens 不要求 max_tokens。
	if _, err := ParseRequest([]byte(`{"model": "m", "messages": [{"role": "user", "content": "x"}]}`), true); err != nil {
		t.Errorf("count_tokens 不该要求 max_tokens：%v", err)
	}
}

// jsonNoEscape 编码成 JSON，不把 < > & 转义成 \u 形式。
func jsonNoEscape(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(b.String())
}
