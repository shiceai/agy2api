package antigravity

import (
	"encoding/json"
	"testing"
)

// envelopeRequest 构造信封并返回其中的 request 对象。
func envelopeRequest(t *testing.T, body string) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	raw, err := BuildEnvelope(BuildParams{ProjectID: "p", WireModel: "gemini-2.5-flash", GeminiRequest: req})
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Request map[string]any `json:"request"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env.Request
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// snake_case 只在 proto 字段层级归一，用户数据（schema 属性名、函数参数）不动。
func TestNormalizeFieldNames(t *testing.T) {
	req := envelopeRequest(t, `{
		"system_instruction": {"parts": [{"text": "sys"}]},
		"generation_config": {
			"max_output_tokens": 100, "maxOutputTokens": 200,
			"thinking_config": {"include_thoughts": true},
			"response_schema": {"type": "object", "properties": {"user_name": {"type": "string"}}}
		},
		"tool_config": {"function_calling_config": {"mode": "AUTO", "allowed_function_names": ["f"]}},
		"safety_settings": [{"category": "x"}],
		"contents": [
			{"role": "user", "parts": [{"inline_data": {"mime_type": "image/png", "data": "AA=="}}]},
			{"role": "model", "parts": [{"function_call": {"name": "f", "args": {"city_name": "x"}}}]},
			{"role": "user", "parts": [{"function_response": {"name": "f", "response": {"result_value": 1}}}]}
		]
	}`)

	want := map[string]string{
		"systemInstruction": `{"parts":[{"text":"sys"}]}`,
		// 两种写法同时存在时保留 camelCase；responseSchema 里的 user_name 不动。
		"generationConfig": `{"maxOutputTokens":200,"responseSchema":{"properties":{"user_name":{"type":"string"}},"type":"object"},"thinkingConfig":{"includeThoughts":true}}`,
		"toolConfig":       `{"functionCallingConfig":{"allowedFunctionNames":["f"],"mode":"AUTO"}}`,
		"contents": `[{"parts":[{"inlineData":{"data":"AA==","mimeType":"image/png"}}],"role":"user"},` +
			`{"parts":[{"functionCall":{"args":{"city_name":"x"},"name":"f"},"thoughtSignature":"skip_thought_signature_validator"}],"role":"model"},` +
			`{"parts":[{"functionResponse":{"name":"f","response":{"result_value":1}}}],"role":"user"}]`,
	}
	for key, w := range want {
		if got := mustJSON(t, req[key]); got != w {
			t.Errorf("%s：\n实际 %s\n期望 %s", key, got, w)
		}
	}
	for _, snake := range []string{"system_instruction", "generation_config", "tool_config", "safety_settings", "safetySettings"} {
		if _, ok := req[snake]; ok {
			t.Errorf("不应保留 %s", snake)
		}
	}
}

// 空 part 和因此变空的消息去掉；text 为数组或非字符串时转成字符串；只有签名的空 text 保留。
func TestNormalizeParts(t *testing.T) {
	req := envelopeRequest(t, `{"contents": [
		{"role": "user", "parts": [{"text": ""}, {}, {"text": null}, {"thought": true}]},
		{"role": "user", "parts": [{"text": ["a", {"type": "text", "text": "b"}, null, 3]}, {"text": 42}]},
		{"role": "model", "parts": [{"text": "", "thoughtSignature": "sig"}, "bogus"]},
		{"role": "user"}
	]}`)
	want := `[{"parts":[{"text":"a\nb\n3"},{"text":"42"}],"role":"user"},` +
		`{"parts":[{"text":"","thoughtSignature":"sig"}],"role":"model"},` +
		`{"role":"user"}]`
	if got := mustJSON(t, req["contents"]); got != want {
		t.Fatalf("\n实际 %s\n期望 %s", got, want)
	}
}

func TestSnakeToCamel(t *testing.T) {
	for in, want := range map[string]string{
		"max_output_tokens": "maxOutputTokens",
		"text":              "text",
		"a__b":              "aB",
		"trailing_":         "trailing",
	} {
		if got := snakeToCamel(in); got != want {
			t.Errorf("snakeToCamel(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// 请求里没有的字段不应凭空多出来。
func TestNormalizeKeepsAbsentFields(t *testing.T) {
	req := envelopeRequest(t, `{"systemInstruction": {"role": "system"}}`)
	if _, ok := req["contents"]; ok {
		t.Fatalf("不应多出 contents：%v", req)
	}
	if got := mustJSON(t, req["systemInstruction"]); got != `{"role":"system"}` {
		t.Fatalf("不应多出 parts：%s", got)
	}
}
