package openai

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type responsesEvent struct {
	Type     string         `json:"type"`
	Seq      int            `json:"sequence_number"`
	Item     map[string]any `json:"item"`
	Delta    string         `json:"delta"`
	Response map[string]any `json:"response"`
}

// runResponsesStream 把几块 Gemini 流式响应交给 ResponsesStreamer，返回解析出的事件（检查 event 行和 type 一致、
// sequence_number 连续）。
func runResponsesStream(t *testing.T, req *ResponsesRequest, chunks []string, finish func(*ResponsesStreamer)) []responsesEvent {
	t.Helper()
	var buf strings.Builder
	s := NewResponsesStreamer(&buf, nil, req)
	for _, c := range chunks {
		s.Chunk([]byte(c))
	}
	finish(s)
	var events []responsesEvent
	for _, block := range strings.Split(strings.TrimSpace(buf.String()), "\n\n") {
		name, data, ok := strings.Cut(block, "\n")
		if !ok {
			t.Fatalf("bad block %q", block)
		}
		var e responsesEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &e); err != nil {
			t.Fatal(err)
		}
		if name != "event: "+e.Type || e.Seq != len(events) {
			t.Fatalf("event %d: %q seq %d type %s", len(events), name, e.Seq, e.Type)
		}
		events = append(events, e)
	}
	return events
}

func eventTypes(events []responsesEvent) string {
	var out []string
	for _, e := range events {
		out = append(out, strings.TrimPrefix(e.Type, "response."))
	}
	return strings.Join(out, ",")
}

// 去掉随机的 id、把网关生成的 call_id（call_ 加 24 位）换成 CALL，便于比较条目。
func stripIDs(t *testing.T, v any) string {
	t.Helper()
	raw, _ := json.Marshal(v)
	var x any
	_ = json.Unmarshal(raw, &x)
	var walk func(any)
	walk = func(v any) {
		switch m := v.(type) {
		case map[string]any:
			delete(m, "id")
			if id, _ := m["call_id"].(string); strings.HasPrefix(id, "call_") && len(id) == 29 {
				m["call_id"] = "CALL"
			}
			for _, c := range m {
				walk(c)
			}
		case []any:
			for _, c := range m {
				walk(c)
			}
		}
	}
	walk(x)
	return asJSON(t, x)
}

// Gemini：思考文字 → reasoning 的 summary；签名挂在工具调用上，要了 encrypted_content 时在工具调用前插一条只带签名的
// reasoning；命名空间工具拆回命名空间和函数名。最后的 response.completed 带全部条目和用量。
func TestResponsesStreamGemini(t *testing.T) {
	req := &ResponsesRequest{Model: "gemini-3-flash", EncryptedReasoning: true, Echo: map[string]any{"tool_choice": "auto"},
		Tools: map[string]ToolRef{"multi_agent_v1__spawn_agent": {Namespace: "multi_agent_v1", Name: "spawn_agent"}}}
	chunks := []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Let me ","thought":true}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"think.","thought":true}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Spawning."}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"multi_agent_v1__spawn_agent","args":{"task":"x"}},"thoughtSignature":"SIG_G"},` +
			`{"functionCall":{"name":"exec_command","args":{"cmd":"ls"}},"thoughtSignature":"skip_thought_signature_validator"}]},"finishReason":"STOP"}],` +
			`"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"thoughtsTokenCount":3,"cachedContentTokenCount":6}}`,
	}
	events := runResponsesStream(t, req, chunks, (*ResponsesStreamer).Finish)
	want := "created,in_progress," +
		"output_item.added,reasoning_summary_part.added,reasoning_summary_text.delta,reasoning_summary_text.delta,reasoning_summary_text.done,reasoning_summary_part.done,output_item.done," +
		"output_item.added,content_part.added,output_text.delta,output_text.done,content_part.done,output_item.done," +
		"output_item.added,output_item.done," +
		"output_item.added,function_call_arguments.delta,function_call_arguments.done,output_item.done," +
		"output_item.added,function_call_arguments.delta,function_call_arguments.done,output_item.done,completed"
	if got := eventTypes(events); got != want {
		t.Fatalf("events =\n%s\nwant\n%s", got, want)
	}
	final := events[len(events)-1].Response
	wantOutput := `[{"summary":[{"text":"Let me think.","type":"summary_text"}],"type":"reasoning"},` +
		`{"content":[{"annotations":[],"logprobs":[],"text":"Spawning.","type":"output_text"}],"role":"assistant","status":"completed","type":"message"},` +
		`{"encrypted_content":"SIG_G","summary":[],"type":"reasoning"},` +
		`{"arguments":"{\"task\":\"x\"}","call_id":"CALL","name":"spawn_agent","namespace":"multi_agent_v1","status":"completed","type":"function_call"},` +
		`{"arguments":"{\"cmd\":\"ls\"}","call_id":"CALL","name":"exec_command","status":"completed","type":"function_call"}]`
	if got := stripIDs(t, final["output"]); got != wantOutput {
		t.Fatalf("output =\n%s\nwant\n%s", got, wantOutput)
	}
	if final["status"] != "completed" || final["model"] != "gemini-3-flash" || final["tool_choice"] != "auto" || final["object"] != "response" {
		t.Fatalf("response = %s", asJSON(t, final))
	}
	if got := asJSON(t, final["usage"]); got != `{"input_tokens":10,"input_tokens_details":{"cached_tokens":6},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":17}` {
		t.Fatalf("usage = %s", got)
	}
	// response.created 时输出为空、没有用量。
	if created := events[0].Response; asJSON(t, created["output"]) != `[]` || created["usage"] != nil || created["status"] != "in_progress" {
		t.Fatalf("created = %s", asJSON(t, created))
	}
	// 增量事件带条目 id 和 output_index，和 added 的一致。
	if events[2].Item["id"] == "" || events[4].Delta != "Let me " {
		t.Fatalf("reasoning events = %+v %+v", events[2], events[4])
	}

	// 没要 encrypted_content：不插只带签名的 reasoning。
	req.EncryptedReasoning = false
	events = runResponsesStream(t, req, chunks, (*ResponsesStreamer).Finish)
	if got := eventTypes(events); strings.Count(got, "output_item.added") != 4 {
		t.Fatalf("events = %s", got)
	}
}

// Claude：签名在思考的最后一块（文字为空）上，进 reasoning 的 encrypted_content；自定义工具的调用转回 custom_tool_call。
func TestResponsesStreamClaudeAndCustomTool(t *testing.T) {
	req := &ResponsesRequest{Model: "claude-sonnet-5-5", EncryptedReasoning: true,
		Tools: map[string]ToolRef{"apply_patch": {Name: "apply_patch", Custom: true}}}
	chunks := []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Plan the patch.","thought":true}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"","thought":true,"thoughtSignature":"SIG_C"}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"toolu_1","name":"apply_patch","args":{"input":"*** Begin Patch"}}}]},"finishReason":"STOP"}]}`,
	}
	events := runResponsesStream(t, req, chunks, (*ResponsesStreamer).Finish)
	want := "created,in_progress," +
		"output_item.added,reasoning_summary_part.added,reasoning_summary_text.delta,reasoning_summary_text.done,reasoning_summary_part.done,output_item.done," +
		"output_item.added,custom_tool_call_input.delta,custom_tool_call_input.done,output_item.done,completed"
	if got := eventTypes(events); got != want {
		t.Fatalf("events = %s", got)
	}
	wantOutput := `[{"encrypted_content":"SIG_C","summary":[{"text":"Plan the patch.","type":"summary_text"}],"type":"reasoning"},` +
		`{"call_id":"toolu_1","input":"*** Begin Patch","name":"apply_patch","status":"completed","type":"custom_tool_call"}]`
	if got := stripIDs(t, events[len(events)-1].Response["output"]); got != wantOutput {
		t.Fatalf("output = %s", got)
	}
	if events[9].Delta != "*** Begin Patch" {
		t.Fatalf("custom input delta = %+v", events[9])
	}
}

// 输出上限是 response.incomplete；上游中途出错是 response.failed（错误码按状态码），之后不再输出。
func TestResponsesStreamIncompleteAndFailed(t *testing.T) {
	req := &ResponsesRequest{Model: "m"}
	events := runResponsesStream(t, req, []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]},"finishReason":"MAX_TOKENS"}]}`,
	}, (*ResponsesStreamer).Finish)
	last := events[len(events)-1]
	if last.Type != "response.incomplete" || last.Response["status"] != "incomplete" ||
		asJSON(t, last.Response["incomplete_details"]) != `{"reason":"max_output_tokens"}` {
		t.Fatalf("last = %+v", last)
	}

	for status, code := range map[int]string{http.StatusTooManyRequests: "rate_limit_exceeded", http.StatusBadGateway: "server_error", http.StatusBadRequest: "invalid_prompt"} {
		events = runResponsesStream(t, req, []string{
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}]}`,
		}, func(s *ResponsesStreamer) {
			s.Error(status, "upstream broke")
			s.Finish() // 已经结束，不再输出
		})
		if got := eventTypes(events); got != "created,in_progress,output_item.added,content_part.added,output_text.delta,output_text.done,content_part.done,output_item.done,failed" {
			t.Fatalf("events = %s", got)
		}
		failed := events[len(events)-1].Response
		if failed["status"] != "failed" || asJSON(t, failed["error"]) != `{"code":"`+code+`","message":"upstream broke"}` {
			t.Fatalf("failed = %s", asJSON(t, failed))
		}
	}
}

// 非流式和流式拼出同样的条目。
func TestConvertResponsesResponse(t *testing.T) {
	req := &ResponsesRequest{Model: "gemini-3-flash", EncryptedReasoning: true, Echo: map[string]any{"store": false}}
	raw := `{"candidates":[{"content":{"role":"model","parts":[` +
		`{"text":"Thinking.","thought":true},{"text":"Answer "},{"text":"here."},` +
		`{"functionCall":{"name":"lookup","args":{"q":  "x"}},"thoughtSignature":"SIG"}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`
	out, err := ConvertResponsesResponse([]byte(raw), req)
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if id, _ := resp["id"].(string); !strings.HasPrefix(id, "resp_") || resp["status"] != "completed" || resp["object"] != "response" {
		t.Fatalf("response = %s", out)
	}
	events := runResponsesStream(t, req, []string{raw}, (*ResponsesStreamer).Finish)
	streamed := stripIDs(t, events[len(events)-1].Response["output"])
	if got := stripIDs(t, resp["output"]); got != streamed || !strings.Contains(got, `"arguments":"{\"q\":\"x\"}"`) || !strings.Contains(got, `"text":"Answer here."`) {
		t.Fatalf("output =\n%s\nstreamed\n%s", got, streamed)
	}
	if got := asJSON(t, resp["usage"]); got != `{"input_tokens":5,"input_tokens_details":{"cached_tokens":0},"output_tokens":2,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":7}` {
		t.Fatalf("usage = %s", got)
	}
}
