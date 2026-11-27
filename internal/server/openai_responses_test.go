package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shiceai/agy2api/internal/store"
)

// 非流式：回 Responses 的响应对象；用量记 openai_responses 接口，会话按 prompt_cache_key 认。
func TestResponsesNonStream(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := openaiHeaders(t, srv)

	resp := doReq(t, srv, http.MethodPost, "/v1/responses", h,
		`{"model":"gemini-3-flash","prompt_cache_key":"sess-1","input":[{"role":"user","content":[{"type":"input_text","text":"hello there"}]}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(resp))
	}
	var r struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Status string `json:"status"`
		Model  string `json:"model"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	decode(t, resp, &r)
	if !strings.HasPrefix(r.ID, "resp_") || r.Object != "response" || r.Status != "completed" || r.Model != "gemini-3-flash" || len(r.Output) != 1 {
		t.Fatalf("response = %+v", r)
	}
	if o := r.Output[0]; o.Type != "message" || o.Role != "assistant" || len(o.Content) != 1 || o.Content[0].Text != "echo: hello there" {
		t.Fatalf("output = %+v", o)
	}
	if r.Usage.InputTokens != 2 || r.Usage.OutputTokens != 3 || r.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", r.Usage)
	}

	svc.Close(t.Context()) // 写完排队的用量记录
	var logs struct {
		Records []struct {
			ID           int64  `json:"id"`
			RequestModel string `json:"request_model"`
			Protocol     string `json:"protocol"`
		} `json:"records"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/logs?protocol=openai_responses", nil, ""), &logs)
	if len(logs.Records) != 1 || logs.Records[0].RequestModel != "gemini-3-flash" || logs.Records[0].Protocol != store.ProtocolOpenAIResponses {
		t.Fatalf("usage = %+v", logs.Records)
	}
	var one struct {
		Record struct {
			Detail store.UsageDetail `json:"detail"`
		} `json:"record"`
	}
	decode(t, doReq(t, srv, http.MethodGet, fmt.Sprintf("/api/admin/usage/logs/%d", logs.Records[0].ID), nil, ""), &one)
	if a := one.Record.Detail.Affinity; a == nil || a.Source != store.SessionPromptCacheKey {
		t.Fatalf("affinity = %+v", a)
	}
}

// 流式：event 行加 data 行，最后是带完整输出的 response.completed。
func TestResponsesStream(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := openaiHeaders(t, srv)

	resp := doReq(t, srv, http.MethodPost, "/v1/responses", h, `{"model":"gemini-3-flash","stream":true,"input":"stream me"}`)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d %s: %s", resp.StatusCode, resp.Header.Get("Content-Type"), readBody(resp))
	}
	var types []string
	var deltas strings.Builder
	var final struct {
		Status string `json:"status"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	for _, block := range strings.Split(strings.TrimSpace(readBody(resp)), "\n\n") {
		name, data, _ := strings.Cut(block, "\n")
		var e struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &e); err != nil || name != "event: "+e.Type {
			t.Fatalf("block %q: %v", block, err)
		}
		types = append(types, strings.TrimPrefix(e.Type, "response."))
		if e.Type == "response.output_text.delta" {
			deltas.WriteString(e.Delta)
		}
		if e.Type == "response.completed" {
			if err := json.Unmarshal(e.Response, &final); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := "created,in_progress,output_item.added,content_part.added,output_text.delta,output_text.delta,output_text.done,content_part.done,output_item.done,completed"
	if got := strings.Join(types, ","); got != want {
		t.Fatalf("events = %s", got)
	}
	if deltas.String() != "echo: stream me" || final.Status != "completed" || len(final.Output) != 1 ||
		final.Output[0].Content[0].Text != "echo: stream me" || final.Usage.InputTokens != 2 {
		t.Fatalf("deltas %q final %+v", deltas.String(), final)
	}
}

// 错误用 OpenAI 的格式：未鉴权、不支持的 previous_response_id、模型不存在。
func TestResponsesErrors(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := openaiHeaders(t, srv)
	check := func(resp *http.Response, status int, errType, msg string) {
		t.Helper()
		var body struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		decode(t, resp, &body)
		if resp.StatusCode != status || body.Error.Type != errType || !strings.Contains(body.Error.Message, msg) {
			t.Fatalf("status %d body %+v，应为 %d %s %q", resp.StatusCode, body, status, errType, msg)
		}
	}
	check(doReq(t, srv, http.MethodPost, "/v1/responses", nil, `{"model":"gemini-3-flash","input":"x"}`),
		http.StatusUnauthorized, "authentication_error", "")
	check(doReq(t, srv, http.MethodPost, "/v1/responses", h, `{"model":"gemini-3-flash","input":"x","previous_response_id":"resp_1"}`),
		http.StatusBadRequest, "invalid_request_error", "previous_response_id")
	check(doReq(t, srv, http.MethodPost, "/v1/responses", h, `{"model":"no-such-model","input":"x"}`),
		http.StatusNotFound, "not_found_error", "no-such-model")
}

// 客户端兼容默认开：Responses 入站的 instructions 和开头的 developer 消息降为开头的用户消息（mock 回显它）；
// 关掉后回到 systemInstruction，mock 回显真正的用户消息。
func TestResponsesSystemAsUser(t *testing.T) {
	svc, _ := newTestService(t)
	svc.relay.SetSystemPrompt("", "") // 关掉默认开启的提示词注入，只测"客户端兼容"的 system 降级
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := openaiHeaders(t, srv)

	read := func() string {
		resp := doReq(t, srv, http.MethodPost, "/v1/responses", h, `{"model":"gemini-3-flash","instructions":"You are Codex.",`+
			`"input":[{"role":"developer","content":"Be precise."},{"role":"user","content":"hello there"}]}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, readBody(resp))
		}
		var r struct {
			Output []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		decode(t, resp, &r)
		if len(r.Output) != 1 || len(r.Output[0].Content) != 1 {
			t.Fatalf("output = %+v", r.Output)
		}
		return r.Output[0].Content[0].Text
	}

	if got := read(); !strings.Contains(got, "client application's own system instructions") {
		t.Fatalf("兼容默认开时 system 应降为开头用户消息，mock 回显 = %q", got)
	}
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/anthropic-compat", nil, `{"system_as_user":false}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("save compat: %d %s", resp.StatusCode, readBody(resp))
	}
	if got := read(); got != "echo: hello there" {
		t.Fatalf("兼容关闭时应回显真正的用户消息，实际 %q", got)
	}
}
