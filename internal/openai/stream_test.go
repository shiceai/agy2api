package openai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// parseChunks 拆出 SSE 的每个 data，最后的 [DONE] 原样返回为 nil。
func parseChunks(t *testing.T, raw string) (chunks []map[string]any, done bool) {
	t.Helper()
	for _, block := range strings.Split(strings.TrimSpace(raw), "\n\n") {
		data, ok := strings.CutPrefix(block, "data: ")
		if !ok {
			t.Fatalf("bad block %q", block)
		}
		if data == "[DONE]" {
			done = true
			continue
		}
		if done {
			t.Fatalf("[DONE] 之后还有数据：%s", data)
		}
		var c map[string]any
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatalf("bad data %q: %v", data, err)
		}
		chunks = append(chunks, c)
	}
	return chunks, done
}

func choice(c map[string]any) (delta map[string]any, finish any) {
	ch := c["choices"].([]any)[0].(map[string]any)
	return ch["delta"].(map[string]any), ch["finish_reason"]
}

// 一段流：role → 思考 → 文字 → 两个工具调用（index 递增）→ finish_reason → 用量 → [DONE]。
func TestStreamerChunks(t *testing.T) {
	var buf bytes.Buffer
	flushes := 0
	s := NewStreamer(&buf, func() { flushes++ }, "gemini-3-flash", true)
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"text":"hmm","thought":true},{"text":"","thought":true,"thoughtSignature":"sig"}]}}],"usageMetadata":{"promptTokenCount":10}}`))
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}`))
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"a","args":{"x":1}},"thoughtSignature":"sig"},{"functionCall":{"id":"toolu_1","name":"b"}}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":3}}`))
	s.Finish()
	s.Finish() // 重复调用不再输出

	chunks, done := parseChunks(t, buf.String())
	if !done || len(chunks) != 7 || flushes != 8 {
		t.Fatalf("chunks = %d done = %v flushes = %d\n%s", len(chunks), done, flushes, buf.String())
	}
	id := chunks[0]["id"]
	for i, c := range chunks {
		if c["id"] != id || c["object"] != "chat.completion.chunk" || c["model"] != "gemini-3-flash" {
			t.Fatalf("chunk %d = %v", i, c)
		}
		if _, ok := c["usage"]; !ok {
			t.Fatalf("include_usage 时每块都应有 usage 字段：%v", c)
		}
	}
	if d, _ := choice(chunks[0]); d["role"] != "assistant" || d["content"] != "" {
		t.Fatalf("first = %v", d)
	}
	if d, _ := choice(chunks[1]); d["reasoning_content"] != "hmm" {
		t.Fatalf("reasoning = %v", d)
	}
	if d, _ := choice(chunks[2]); d["content"] != "Hi" {
		t.Fatalf("content = %v", d)
	}
	for i, want := range []string{"a", "b"} {
		d, _ := choice(chunks[3+i])
		call := d["tool_calls"].([]any)[0].(map[string]any)
		fn := call["function"].(map[string]any)
		if call["index"] != float64(i) || fn["name"] != want || call["type"] != "function" || call["id"] == "" {
			t.Fatalf("tool call %d = %v", i, call)
		}
	}
	if d, finish := choice(chunks[5]); len(d) != 0 || finish != "tool_calls" || chunks[5]["usage"] != nil {
		t.Fatalf("finish chunk = %v", chunks[5])
	}
	last := chunks[6]
	usage := last["usage"].(map[string]any)
	if len(last["choices"].([]any)) != 0 || usage["prompt_tokens"] != float64(10) || usage["completion_tokens"] != float64(8) {
		t.Fatalf("usage chunk = %v", last)
	}
}

// 不要用量时没有 usage 字段，也没有用量块；空流也有 role 块和 finish 块。
func TestStreamerWithoutUsage(t *testing.T) {
	var buf bytes.Buffer
	s := NewStreamer(&buf, nil, "m", false)
	s.Finish()
	chunks, done := parseChunks(t, buf.String())
	if !done || len(chunks) != 2 {
		t.Fatalf("chunks = %v", chunks)
	}
	for _, c := range chunks {
		if _, ok := c["usage"]; ok {
			t.Fatalf("不要用量时不该有 usage：%v", c)
		}
	}
	if _, finish := choice(chunks[1]); finish != "stop" {
		t.Fatalf("finish = %v", finish)
	}
}

// 流中途出错：发 OpenAI 格式的错误块，之后不再输出，也没有 [DONE]。
func TestStreamerError(t *testing.T) {
	var buf bytes.Buffer
	s := NewStreamer(&buf, nil, "m", false)
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`))
	s.Error(503, "upstream is temporarily unavailable")
	s.Finish()
	chunks, done := parseChunks(t, buf.String())
	if done || len(chunks) != 3 {
		t.Fatalf("chunks = %v done = %v", chunks, done)
	}
	e := chunks[2]["error"].(map[string]any)
	if e["type"] != "server_error" || e["message"] != "upstream is temporarily unavailable" {
		t.Fatalf("error = %v", e)
	}
}
