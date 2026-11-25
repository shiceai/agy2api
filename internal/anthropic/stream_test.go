package anthropic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type sseEvent struct {
	name string
	data map[string]any
}

func parseEvents(t *testing.T, raw string) []sseEvent {
	t.Helper()
	var out []sseEvent
	sc := bufio.NewScanner(strings.NewReader(raw))
	var name string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var d map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d); err != nil {
				t.Fatalf("bad data %q: %v", line, err)
			}
			if d["type"] != name {
				t.Fatalf("event %q has data type %v", name, d["type"])
			}
			out = append(out, sseEvent{name, d})
		}
	}
	return out
}

func describe(events []sseEvent) string {
	var parts []string
	for _, e := range events {
		s := e.name
		switch e.name {
		case "content_block_start":
			s += ":" + e.data["content_block"].(map[string]any)["type"].(string)
		case "content_block_delta":
			s += ":" + e.data["delta"].(map[string]any)["type"].(string)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// 一段 Claude 风格的流：思考（最后一块带签名）→ 文字 → 工具调用；Gemini 风格的工具调用带签名时前面插签名块。
func TestStreamerEvents(t *testing.T) {
	var buf bytes.Buffer
	flushes := 0
	s := NewStreamer(&buf, func() { flushes++ }, "claude-opus-5-5")
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"text":"think ","thought":true}]}}],"usageMetadata":{"promptTokenCount":50}}`))
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"text":"more","thought":true}]}}]}`))
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"text":"","thought":true,"thoughtSignature":"sigC"}]}}]}`))
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}`))
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"text":" there"}]}}]}`))
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"toolu_vrtx_01x","name":"f","args":{"a":1}}}]}}]}`))
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"g","args":{}},"thoughtSignature":"sigG"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":9,"thoughtsTokenCount":4}}`))
	s.Finish()
	s.Finish() // 重复调用无副作用
	if s.Err() != nil {
		t.Fatal(s.Err())
	}
	events := parseEvents(t, buf.String())
	want := "message_start " +
		"content_block_start:thinking content_block_delta:thinking_delta content_block_delta:thinking_delta content_block_delta:signature_delta content_block_stop " +
		"content_block_start:text content_block_delta:text_delta content_block_delta:text_delta content_block_stop " +
		"content_block_start:tool_use content_block_delta:input_json_delta content_block_stop " +
		"content_block_start:thinking content_block_delta:signature_delta content_block_stop " +
		"content_block_start:tool_use content_block_delta:input_json_delta content_block_stop " +
		"message_delta message_stop"
	if got := describe(events); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if flushes != len(events) {
		t.Fatalf("flush %d 次，事件 %d 个", flushes, len(events))
	}
	// 块序号连续递增，同一个块的 start、delta、stop 序号相同。
	idx := -1
	for _, e := range events {
		if e.name == "content_block_start" {
			idx++
		}
		if i, ok := e.data["index"].(float64); ok && int(i) != idx {
			t.Fatalf("%s index = %v，应为 %d", e.name, i, idx)
		}
	}
	start := events[0].data["message"].(map[string]any)
	if start["model"] != "claude-opus-5-5" || start["usage"].(map[string]any)["input_tokens"] != float64(50) {
		t.Fatalf("message_start = %v", start)
	}
	if !idPattern.MatchString(start["id"].(string)) {
		t.Fatalf("message id = %v", start["id"])
	}
	md := events[len(events)-2].data
	if md["delta"].(map[string]any)["stop_reason"] != "tool_use" || md["usage"].(map[string]any)["output_tokens"] != float64(13) {
		t.Fatalf("message_delta = %v", md)
	}
	if events[11].data["delta"].(map[string]any)["partial_json"] != `{"a":1}` {
		t.Fatalf("input_json_delta = %v", events[11].data)
	}
}

func TestStreamerEmptyAndError(t *testing.T) {
	var buf bytes.Buffer
	s := NewStreamer(&buf, nil, "m")
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"text":"","thought":true}]}}]}`)) // 空的思考 part 不开块
	s.Finish()
	if got := describe(parseEvents(t, buf.String())); got != "message_start message_delta message_stop" {
		t.Fatalf("empty stream: %s", got)
	}

	buf.Reset()
	s = NewStreamer(&buf, nil, "m")
	s.Chunk([]byte(`{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`))
	s.Error(429, "rate limited")
	s.Finish() // 出错后不再输出
	events := parseEvents(t, buf.String())
	if got := describe(events); got != "message_start content_block_start:text content_block_delta:text_delta error" {
		t.Fatalf("error stream: %s", got)
	}
	if e := events[len(events)-1].data["error"].(map[string]any); e["type"] != "rate_limit_error" || e["message"] != "rate limited" {
		t.Fatalf("error event = %v", e)
	}
}
