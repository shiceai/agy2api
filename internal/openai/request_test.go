package openai

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func mustParse(t *testing.T, body string) *Request {
	t.Helper()
	req, err := ParseRequest([]byte(body))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	return req
}

// 转成 JSON 再比较，避免 json.Number 和 float64 的差别；不转义 <>，期望值好写。
func asJSON(t *testing.T, v any) string {
	t.Helper()
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// 开头的 system、developer 进 systemInstruction，中间的转成 <system-reminder> 并入相邻的用户消息。
func TestParseRequestBasics(t *testing.T) {
	req := mustParse(t, `{
		"model": "gemini-3-flash", "stream": true, "stream_options": {"include_usage": true},
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "developer", "content": [{"type": "text", "text": "Be brief."}]},
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "hello"},
			{"role": "system", "content": "Today is Monday."},
			{"role": "user", "content": [{"type": "text", "text": "what's 2+2?"}]}
		],
		"max_completion_tokens": 1024, "max_tokens": 10, "temperature": 0.5, "top_p": 0.9, "stop": "END", "n": 1,
		"presence_penalty": 0.1, "user": "u1"
	}`)
	if req.Model != "gemini-3-flash" || !req.Stream || !req.IncludeUsage {
		t.Fatalf("model/stream/usage = %q %v %v", req.Model, req.Stream, req.IncludeUsage)
	}
	if got := asJSON(t, req.Body["systemInstruction"]); got != `{"parts":[{"text":"You are helpful."},{"text":"Be brief."}],"role":"user"}` {
		t.Fatalf("systemInstruction = %s", got)
	}
	want := `[{"parts":[{"text":"hi"}],"role":"user"},{"parts":[{"text":"hello"}],"role":"model"},` +
		`{"parts":[{"text":"<system-reminder>\nToday is Monday.\n</system-reminder>"},{"text":"what's 2+2?"}],"role":"user"}]`
	if got := asJSON(t, req.Body["contents"]); got != want {
		t.Fatalf("contents = %s", got)
	}
	// max_completion_tokens 优先于 max_tokens；不认的参数忽略。
	if got := asJSON(t, req.Body["generationConfig"]); got != `{"maxOutputTokens":1024,"stopSequences":["END"],"temperature":0.5,"topP":0.9}` {
		t.Fatalf("generationConfig = %s", got)
	}
}

// 工具声明、tool_choice、助手的工具调用和 tool 结果（名字按 tool_call_id 查回来）。
func TestParseRequestTools(t *testing.T) {
	req := mustParse(t, `{
		"model": "m",
		"tools": [
			{"type": "function", "function": {"name": "get_weather", "description": "Weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}},
			{"type": "function", "function": {"name": "now"}},
			{"type": "web_search"}
		],
		"tool_choice": {"type": "function", "function": {"name": "get_weather"}},
		"messages": [
			{"role": "user", "content": "weather?"},
			{"role": "assistant", "content": null, "reasoning_content": "let me check", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Paris\",\"n\":12345678901234567890}"}},
				{"id": "call_2", "type": "function", "function": {"name": "now", "arguments": ""}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "sunny"},
			{"role": "tool", "tool_call_id": "call_2", "content": [{"type": "text", "text": "noon"}]}
		]
	}`)
	wantTools := `[{"functionDeclarations":[{"description":"Weather","name":"get_weather","parameters":{"properties":{"city":{"type":"string"}},"type":"object"}},` +
		`{"name":"now","parameters":{"properties":{},"type":"object"}}]}]`
	if got := asJSON(t, req.Body["tools"]); got != wantTools {
		t.Fatalf("tools = %s", got)
	}
	if got := asJSON(t, req.Body["toolConfig"]); got != `{"functionCallingConfig":{"allowedFunctionNames":["get_weather"],"mode":"ANY"}}` {
		t.Fatalf("toolConfig = %s", got)
	}
	want := `[{"parts":[{"text":"weather?"}],"role":"user"},` +
		`{"parts":[{"functionCall":{"args":{"city":"Paris","n":12345678901234567890},"id":"call_1","name":"get_weather"}},{"functionCall":{"args":{},"id":"call_2","name":"now"}}],"role":"model"},` +
		`{"parts":[{"functionResponse":{"id":"call_1","name":"get_weather","response":{"result":"sunny"}}},{"functionResponse":{"id":"call_2","name":"now","response":{"result":"noon"}}}],"role":"user"}]`
	if got := asJSON(t, req.Body["contents"]); got != want {
		t.Fatalf("contents = %s", got)
	}
	if got := strings.Join(req.Dropped, ","); got != "reasoning_content in history,tool type web_search" {
		t.Fatalf("dropped = %s", got)
	}
}

func TestToolChoice(t *testing.T) {
	for choice, want := range map[string]string{
		`"auto"`:     `{"functionCallingConfig":{"mode":"AUTO"}}`,
		`"none"`:     `{"functionCallingConfig":{"mode":"NONE"}}`,
		`"required"`: `{"functionCallingConfig":{"mode":"ANY"}}`,
	} {
		req := mustParse(t, `{"model":"m","tool_choice":`+choice+`,"tools":[{"type":"function","function":{"name":"f"}}],"messages":[{"role":"user","content":"x"}]}`)
		if got := asJSON(t, req.Body["toolConfig"]); got != want {
			t.Errorf("tool_choice %s → %s，应为 %s", choice, got, want)
		}
	}
	// 没有工具时不带 toolConfig。
	req := mustParse(t, `{"model":"m","tool_choice":"auto","messages":[{"role":"user","content":"x"}]}`)
	if _, ok := req.Body["toolConfig"]; ok {
		t.Fatal("没有工具时不该有 toolConfig")
	}
}

// 图片（data URL、http URL）、音频、文件（data URL、纯 base64 按扩展名）。
func TestParseRequestMedia(t *testing.T) {
	req := mustParse(t, `{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"look"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR","detail":"high"}},
		{"type":"image_url","image_url":{"url":"https://example.com/a.jpg"}},
		{"type":"input_audio","input_audio":{"data":"UklG","format":"wav"}},
		{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBER","filename":"a.pdf"}},
		{"type":"file","file":{"file_data":"JVBER","filename":"b.pdf"}}
	]}]}`)
	want := `[{"parts":[{"text":"look"},{"inlineData":{"data":"iVBOR","mimeType":"image/png"}},` +
		`{"fileData":{"fileUri":"https://example.com/a.jpg","mimeType":"image/*"}},{"inlineData":{"data":"UklG","mimeType":"audio/wav"}},` +
		`{"inlineData":{"data":"JVBER","mimeType":"application/pdf"}},{"inlineData":{"data":"JVBER","mimeType":"application/pdf"}}],"role":"user"}]`
	if got := asJSON(t, req.Body["contents"]); got != want {
		t.Fatalf("contents = %s", got)
	}
}

func TestParseRequestThinkingAndFormat(t *testing.T) {
	for effort, level := range map[string]string{"none": "low", "minimal": "low", "low": "low", "Medium": "medium", "high": "high", "xhigh": "xhigh"} {
		req := mustParse(t, `{"model":"m","reasoning_effort":"`+effort+`","messages":[{"role":"user","content":"x"}]}`)
		want := `{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"` + level + `"}}`
		if got := asJSON(t, req.Body["generationConfig"]); got != want {
			t.Errorf("reasoning_effort %s → %s，应为 %s", effort, got, want)
		}
	}
	req := mustParse(t, `{"model":"m","response_format":{"type":"json_object"},"messages":[{"role":"user","content":"x"}]}`)
	if got := asJSON(t, req.Body["generationConfig"]); got != `{"responseMimeType":"application/json"}` {
		t.Fatalf("json_object = %s", got)
	}
	req = mustParse(t, `{"model":"m","response_format":{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"object","properties":{"a":{"type":"integer"}}}}},"messages":[{"role":"user","content":"x"}]}`)
	if got := asJSON(t, req.Body["generationConfig"]); got != `{"responseJsonSchema":{"properties":{"a":{"type":"integer"}},"type":"object"},"responseMimeType":"application/json"}` {
		t.Fatalf("json_schema = %s", got)
	}
}

func TestParseRequestInvalid(t *testing.T) {
	cases := map[string]string{
		`not json`: "invalid JSON",
		`{"messages":[{"role":"user","content":"x"}]}`: "model: field required",
		`{"model":"m"}`: "messages: at least one",
		`{"model":"m","messages":[{"role":"system","content":"x"}]}`:                                                           "no content to send",
		`{"model":"m","messages":[{"role":"function","content":"x"}]}`:                                                         "messages.0.role",
		`{"model":"m","n":2,"messages":[{"role":"user","content":"x"}]}`:                                                       "n: only 1",
		`{"model":"m","max_tokens":0,"messages":[{"role":"user","content":"x"}]}`:                                              "must be at least 1",
		`{"model":"m","reasoning_effort":"max","messages":[{"role":"user","content":"x"}]}`:                                    "reasoning_effort",
		`{"model":"m","response_format":{"type":"xml"},"messages":[{"role":"user","content":"x"}]}`:                            "response_format.type",
		`{"model":"m","messages":[{"role":"tool","content":"x"}]}`:                                                             "messages.0.tool_call_id",
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"ftp://x"}}]}]}`:            "messages.0.content.0.image_url.url",
		`{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-1"}}]}]}`:                   "file_id",
		`{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"abc","filename":"noext"}}]}]}`: "known extension",
		`{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"c","function":{}}]}]}`:                              "function.name",
	}
	for body, want := range cases {
		_, err := ParseRequest([]byte(body))
		var ie *InvalidError
		if !errors.As(err, &ie) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s → %v，应含 %q", body, err, want)
		}
	}
}

// 坏的工具调用参数不拒绝整个请求，换成空对象。
func TestParseRequestBadArguments(t *testing.T) {
	req := mustParse(t, `{"model":"m","messages":[{"role":"user","content":"x"},
		{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{oops"}}]},
		{"role":"tool","tool_call_id":"c1","content":"r"}]}`)
	contents := req.Body["contents"].([]any)
	call := contents[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	if asJSON(t, call["args"]) != `{}` || len(req.Dropped) != 1 {
		t.Fatalf("args = %s dropped = %v", asJSON(t, call["args"]), req.Dropped)
	}
}
