package anthropic

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

var idPattern = regexp.MustCompile(`^msg_vrtx_01[1-9A-HJ-NP-Za-km-z]{22}$`)

func TestConvertResponse(t *testing.T) {
	raw := `{"candidates":[{"content":{"role":"model","parts":[
		{"text":"step 1. ","thought":true},
		{"text":"step 2.","thought":true,"thoughtSignature":"sigC"},
		{"text":"Hello "},{"text":"world"},
		{"functionCall":{"id":"toolu_vrtx_01abc","name":"get_weather","args":{"city":"Paris"}}},
		{"functionCall":{"name":"lookup","args":{"q":1}},"thoughtSignature":"sigG"},
		{"functionCall":{"name":"noop"},"thoughtSignature":"skip_thought_signature_validator"}
	]},"finishReason":"STOP"}],
	"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":30,"candidatesTokenCount":20,"thoughtsTokenCount":5}}`
	out, err := ConvertResponse([]byte(raw), "claude-sonnet-5-5")
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		ID         string           `json:"id"`
		Type       string           `json:"type"`
		Role       string           `json:"role"`
		Model      string           `json:"model"`
		Content    []map[string]any `json:"content"`
		StopReason string           `json:"stop_reason"`
		Usage      Usage            `json:"usage"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatal(err)
	}
	if !idPattern.MatchString(msg.ID) || msg.Type != "message" || msg.Role != "assistant" || msg.Model != "claude-sonnet-5-5" {
		t.Fatalf("header = %+v", msg)
	}
	if msg.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q", msg.StopReason)
	}
	if msg.Usage != (Usage{InputTokens: 70, CacheReadInputTokens: 30, OutputTokens: 25}) {
		t.Fatalf("usage = %+v", msg.Usage)
	}
	types := make([]string, len(msg.Content))
	for i, b := range msg.Content {
		types[i] = b["type"].(string)
	}
	if got := strings.Join(types, ","); got != "thinking,text,tool_use,thinking,tool_use,tool_use" {
		t.Fatalf("content types = %s", got)
	}
	if msg.Content[0]["thinking"] != "step 1. step 2." || msg.Content[0]["signature"] != "sigC" {
		t.Fatalf("thinking = %v", msg.Content[0])
	}
	if msg.Content[1]["text"] != "Hello world" {
		t.Fatalf("text = %v", msg.Content[1])
	}
	if msg.Content[2]["id"] != "toolu_vrtx_01abc" || msg.Content[2]["name"] != "get_weather" {
		t.Fatalf("tool_use = %v", msg.Content[2])
	}
	// Gemini 的签名放在工具调用前的空思考块里；网关自己的占位签名不回给客户端。
	if msg.Content[3]["thinking"] != "" || msg.Content[3]["signature"] != "sigG" {
		t.Fatalf("carrier = %v", msg.Content[3])
	}
	if id, _ := msg.Content[4]["id"].(string); !strings.HasPrefix(id, "toolu_vrtx_01") || len(id) != len("toolu_vrtx_01")+22 {
		t.Fatalf("generated tool id = %q", id)
	}
	if input, _ := json.Marshal(msg.Content[5]["input"]); string(input) != "{}" {
		t.Fatalf("missing args should be {}: %s", input)
	}
}

func TestConvertResponseStopReasons(t *testing.T) {
	for finish, want := range map[string]string{"STOP": "end_turn", "MAX_TOKENS": "max_tokens", "SAFETY": "refusal", "": "end_turn", "OTHER": "end_turn"} {
		out, err := ConvertResponse([]byte(`{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"`+finish+`"}]}`), "m")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out, []byte(`"stop_reason":"`+want+`"`)) {
			t.Errorf("%s: %s", finish, out)
		}
	}
	out, _ := ConvertResponse([]byte(`{"promptFeedback":{"blockReason":"SAFETY"}}`), "m")
	if !bytes.Contains(out, []byte(`"stop_reason":"refusal"`)) || !bytes.Contains(out, []byte(`"content":[]`)) {
		t.Errorf("blocked prompt: %s", out)
	}
}

func TestErrorType(t *testing.T) {
	for status, want := range map[int]string{400: "invalid_request_error", 401: "authentication_error", 403: "permission_error",
		404: "not_found_error", 413: "request_too_large", 429: "rate_limit_error", 503: "overloaded_error", 500: "api_error", 504: "api_error", 422: "invalid_request_error"} {
		if got := ErrorType(status); got != want {
			t.Errorf("ErrorType(%d) = %s，应为 %s", status, got, want)
		}
	}
}
