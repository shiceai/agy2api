package antigravity

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestUnwrapGeminiJSONPassesThroughUpstreamBytes(t *testing.T) {
	raw := []byte(`{"response": {"modelVersion": "m1", "candidates": [{"content": {"parts": [{"text": "a<b> & c"}]}}],
		"usageMetadata": {"promptTokenCount": 3, "candidatesTokenCount": 4, "totalTokenCount": 7}}, "traceId": "x"}`)
	inner, usage := unwrapGeminiJSON(raw)
	// 原样截取：不把 < > & 转成 <，字段顺序不变。
	if !bytes.Contains(inner, []byte(`"a<b> & c"`)) || !bytes.HasPrefix(inner, []byte(`{"modelVersion": "m1"`)) {
		t.Fatalf("应原样截取 response 的字节：%s", inner)
	}
	if bytes.Contains(inner, []byte("traceId")) {
		t.Fatalf("不应包含信封外层字段：%s", inner)
	}
	if usage == nil || usage.UpstreamModel != "m1" || usage.PromptTokens != 3 || usage.OutputTokens != 4 || usage.TotalTokens != 7 {
		t.Fatalf("用量解析不对：%+v", usage)
	}
}

func TestUnwrapGeminiJSONRestoresCPAUsage(t *testing.T) {
	raw := []byte(`{"response":{"candidates":[],"cpaUsageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}}`)
	inner, usage := unwrapGeminiJSON(raw)
	if bytes.Contains(inner, []byte("cpaUsageMetadata")) || !bytes.Contains(inner, []byte(`"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}`)) {
		t.Fatalf("cpaUsageMetadata 应还原为 usageMetadata：%s", inner)
	}
	if usage == nil || usage.TotalTokens != 3 {
		t.Fatalf("缺 totalTokenCount 时应按输入加输出计算：%+v", usage)
	}

	// 已有 usageMetadata 时以它为准，只删掉 cpaUsageMetadata。
	raw = []byte(`{"response":{"usageMetadata":{"totalTokenCount":9},"cpaUsageMetadata":{"totalTokenCount":1}}}`)
	if inner, usage = unwrapGeminiJSON(raw); bytes.Contains(inner, []byte("cpa")) || usage.TotalTokens != 9 {
		t.Fatalf("已有 usageMetadata 时应保留它：%s %+v", inner, usage)
	}
}

func TestUnwrapGeminiJSONNonEnvelope(t *testing.T) {
	for _, raw := range []string{`not json`, `{"candidates":[]}`} {
		if inner, usage := unwrapGeminiJSON([]byte(raw)); string(inner) != raw || usage != nil {
			t.Fatalf("非信封输入应原样返回：%q → %q %+v", raw, inner, usage)
		}
	}
}

// 流式帧输出压成一行，SSE 的 data 字段不会被换行打断；最后一帧的用量可读。
func TestSSEUnwrapperCompactsFrames(t *testing.T) {
	upstream := "data: {\"response\": {\"candidates\": [{\"content\": {\"parts\": [{\"text\": \"hi\"}]}}]}}\n\n" +
		"data: {\"response\":\n" +
		"data:   {\"modelVersion\": \"m2\", \"usageMetadata\": {\"promptTokenCount\": 1, \"candidatesTokenCount\": 2}}}\n\n"
	u := newSSEUnwrapper(io.NopCloser(strings.NewReader(upstream)), false)
	out, err := io.ReadAll(u)
	if err != nil {
		t.Fatal(err)
	}
	want := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n" +
		"data: {\"modelVersion\":\"m2\",\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":2}}\n\n"
	if string(out) != want {
		t.Fatalf("流式输出不对：\n%s\n期望：\n%s", out, want)
	}
	if got := u.usage(); got == nil || got.UpstreamModel != "m2" || got.TotalTokens != 3 {
		t.Fatalf("流式用量不对：%+v", got)
	}
}

func TestDeepCopyMapIsIndependent(t *testing.T) {
	src := map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]any{"text": "a"}}}}, "n": 1.5}
	dst := deepCopyMap(src)
	dst["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"] = "changed"
	dst["n"] = 2.0
	if src["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"] != "a" || src["n"] != 1.5 {
		t.Fatalf("修改副本不应影响原对象：%v", src)
	}
}

// 非流式响应超过上限时报 ErrResponseTooLarge，不把截断的 JSON 当成功结果返回；正好在上限内的照常解包。
func TestNonStreamResponseSizeLimit(t *testing.T) {
	old := nonStreamBodyLimit
	nonStreamBodyLimit = 64
	t.Cleanup(func() { nonStreamBodyLimit = old })

	build := func(body string) (*Response, error) {
		return buildResponse(&http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, false, false)
	}
	fits := `{"response":{"responseId":"r1"}}`
	fits += strings.Repeat(" ", int(nonStreamBodyLimit)-len(fits))
	resp, err := build(fits)
	if err != nil {
		t.Fatalf("正好 %d 字节应能解包：%v", nonStreamBodyLimit, err)
	}
	if got, _ := io.ReadAll(resp.Body); string(got) != `{"responseId":"r1"}` {
		t.Fatalf("解包结果不对：%s", got)
	}
	if _, err := build(fits + " "); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("超过上限应返回 ErrResponseTooLarge，实际 %v", err)
	}
}

// 流式中途的错误帧（HTTP 已经是 200）：之前的帧照常输出，错误帧不转发，Read 返回按 error.code 分类的
// *StreamError，之后的读取一直返回它。
func TestSSEUnwrapperStopsAtErrorFrame(t *testing.T) {
	upstream := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}}\n\n" +
		"data: {\"error\":{\"code\":429,\"status\":\"RESOURCE_EXHAUSTED\",\"message\":\"exhausted\"," +
		"\"details\":[{\"@type\":\"type.googleapis.com/google.rpc.ErrorInfo\",\"reason\":\"QUOTA_EXHAUSTED\"}]}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"after\"}]}}]}}\n\n"
	resp, err := buildResponse(&http.Response{StatusCode: http.StatusOK, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(upstream))}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(resp.Body)
	var se *StreamError
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests || se.Kind != KindQuotaExhausted || se.Message != "exhausted" {
		t.Fatalf("应返回分类好的 *StreamError，实际 %v", err)
	}
	if string(out) != "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n" {
		t.Fatalf("错误帧之前的帧应照常输出，错误帧和之后的不输出：%q", out)
	}
	if resp.StreamError() != se {
		t.Fatalf("StreamError() 应返回同一个错误帧，实际 %v", resp.StreamError())
	}
	if n, again := resp.Body.Read(make([]byte, 8)); n != 0 || again != err {
		t.Fatalf("之后的读取应一直返回错误帧，实际 %d %v", n, again)
	}

	// code 不是 4xx、5xx 时按 502（服务端错误）算。
	u := newSSEUnwrapper(io.NopCloser(strings.NewReader("data: {\"error\":{\"message\":\"boom\"}}\n\n")), false)
	if _, err := io.ReadAll(u); !errors.As(err, &se) || se.Status != http.StatusBadGateway || se.Kind != KindServerError {
		t.Fatalf("没有状态码的错误帧应按 502 算，实际 %v", err)
	}
}

// 非流式 200 但正文是错误：按 error.code 当成非 2xx 响应返回原始错误体，交给调用方分类；正常结果不受影响。
func TestNonStreamInBandError(t *testing.T) {
	body := `{"error":{"code":503,"status":"UNAVAILABLE","message":"overloaded"}}`
	resp, err := buildResponse(&http.Response{StatusCode: http.StatusOK, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(body))}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	if resp.Status != http.StatusServiceUnavailable || string(got) != body || resp.Usage != nil {
		t.Fatalf("应当成 503 返回原始错误体，实际 %d %s", resp.Status, got)
	}
	// 正文里只是有个叫 error 的非对象字段时不算错误。
	resp, err = buildResponse(&http.Response{StatusCode: http.StatusOK, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(`{"response":{"responseId":"r1"},"error":null}`))}, false, false)
	if err != nil || resp.Status != http.StatusOK {
		t.Fatalf("error 为 null 时应照常成功，实际 %v %v", resp, err)
	}
}
