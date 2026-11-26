package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// anthropicHeaders 把 importAndKey 给的 Gemini 头换成 Claude Code 的写法（x-api-key 加 anthropic-version）。
func anthropicHeaders(t *testing.T, srv *httptest.Server) map[string]string {
	h := importAndKey(t, srv)
	return map[string]string{"x-api-key": h["x-goog-api-key"], "anthropic-version": "2023-06-01"}
}

func TestMessagesNonStream(t *testing.T) {
	svc, _ := newTestService(t)
	svc.clientSystemAsUser.Store(false) // 只测转换，不动 system
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := anthropicHeaders(t, srv)

	resp := doReq(t, srv, http.MethodPost, "/v1/messages", h,
		`{"model":"gemini-3-flash","max_tokens":100,"system":"be brief","messages":[{"role":"user","content":"hello there"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(resp))
	}
	var msg struct {
		Type       string `json:"type"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	decode(t, resp, &msg)
	if msg.Type != "message" || msg.Model != "gemini-3-flash" || msg.StopReason != "end_turn" {
		t.Fatalf("message = %+v", msg)
	}
	if len(msg.Content) != 1 || msg.Content[0].Type != "text" || msg.Content[0].Text != "echo: hello there" {
		t.Fatalf("content = %+v", msg.Content)
	}
	if msg.Usage.InputTokens != 2 || msg.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", msg.Usage)
	}
}

func TestMessagesStream(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := anthropicHeaders(t, srv)

	resp := doReq(t, srv, http.MethodPost, "/v1/messages", h,
		`{"model":"gemini-3-flash","max_tokens":100,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"stream me"}]}]}`)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d %s: %s", resp.StatusCode, resp.Header.Get("Content-Type"), readBody(resp))
	}
	body := readBody(resp)
	var names []string
	var text strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			names = append(names, name)
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var ev struct {
				Delta struct {
					Text string `json:"text"`
				} `json:"delta"`
			}
			_ = json.Unmarshal([]byte(data), &ev)
			text.WriteString(ev.Delta.Text)
		}
	}
	want := "message_start content_block_start content_block_delta content_block_delta content_block_stop message_delta message_stop"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("events = %s\nbody:\n%s", got, body)
	}
	if text.String() != "echo: stream me" {
		t.Fatalf("text = %q", text.String())
	}
}

func TestMessagesCountTokensAndModels(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := anthropicHeaders(t, srv)

	resp := doReq(t, srv, http.MethodPost, "/v1/messages/count_tokens", h,
		`{"model":"gemini-3-flash","messages":[{"role":"user","content":"one two three"}]}`)
	var counted struct {
		InputTokens int `json:"input_tokens"`
	}
	decode(t, resp, &counted)
	if counted.InputTokens != 3 {
		t.Fatalf("input_tokens = %d", counted.InputTokens)
	}

	// 带 anthropic-version 的 /v1/models 返回 Anthropic 格式，带 x-goog-api-key 的是 Gemini 格式（其余是 OpenAI 格式，见 openai_test.go）。
	var list struct {
		Data []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/v1/models", h, ""), &list)
	if len(list.Data) != 2 || list.Data[0].ID != "gemini-3-flash-high" || list.Data[1].ID != "gemini-3-pro" || list.Data[0].Type != "model" || list.HasMore {
		t.Fatalf("anthropic models = %+v", list)
	}
	var gemini struct {
		Models []map[string]any `json:"models"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/v1/models", map[string]string{"x-goog-api-key": h["x-api-key"]}, ""), &gemini)
	if len(gemini.Models) != 2 {
		t.Fatalf("gemini models = %+v", gemini)
	}
}

// 错误都是 Messages 的格式：没带 Key 401、请求不合法 400、模型不存在 404；Gemini 接口的错误格式不变。
func TestMessagesErrors(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := anthropicHeaders(t, srv)
	check := func(resp *http.Response, status int, errType string) {
		t.Helper()
		var body struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		decode(t, resp, &body)
		if resp.StatusCode != status || body.Type != "error" || body.Error.Type != errType || body.Error.Message == "" {
			t.Fatalf("status %d body %+v，应为 %d %s", resp.StatusCode, body, status, errType)
		}
	}
	msg := `{"model":"gemini-3-flash","max_tokens":10,"messages":[{"role":"user","content":"x"}]}`
	check(doReq(t, srv, http.MethodPost, "/v1/messages", nil, msg), http.StatusUnauthorized, "authentication_error")
	check(doReq(t, srv, http.MethodPost, "/v1/messages", h, `{"model":"gemini-3-flash","messages":[{"role":"user","content":"x"}]}`),
		http.StatusBadRequest, "invalid_request_error")
	check(doReq(t, srv, http.MethodPost, "/v1/messages", h, strings.Replace(msg, "gemini-3-flash", "no-such-model", 1)),
		http.StatusNotFound, "not_found_error")

	resp := doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-flash:generateContent", nil, `{}`)
	if body := readBody(resp); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `"status":"UNAUTHENTICATED"`) {
		t.Fatalf("gemini error = %d %s", resp.StatusCode, body)
	}
}

// 别名：自定义规则把 Claude Code 的 Haiku 名字映射到号池里的模型；用量记客户端原名和 anthropic 接口。
func TestMessagesModelAlias(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := anthropicHeaders(t, srv)

	resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/model-aliases", nil,
		`{"use_presets":true,"rules":"# Haiku 用 Flash\nclaude-*haiku* => gemini-3-flash"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save aliases: %d %s", resp.StatusCode, readBody(resp))
	}
	resp = doReq(t, srv, http.MethodPost, "/v1/messages", h,
		`{"model":"claude-haiku-5-5-20261001","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("aliased request: %d %s", resp.StatusCode, readBody(resp))
	}
	var msg struct {
		Model string `json:"model"`
	}
	decode(t, resp, &msg)
	if msg.Model != "claude-haiku-5-5-20261001" {
		t.Fatalf("响应里的 model 应原样回传：%q", msg.Model)
	}

	var test struct {
		Target   string     `json:"target"`
		Rule     *aliasRule `json:"rule"`
		Resolved string     `json:"resolved"`
	}
	decode(t, doReq(t, srv, http.MethodPost, "/api/admin/settings/model-aliases/test", nil, `{"model":"Claude-Haiku-5-5[1m]"}`), &test)
	if test.Target != "gemini-3-flash" || test.Rule == nil || test.Rule.Pattern != "claude-*haiku*" || test.Resolved != "gemini-3-flash-high" {
		t.Fatalf("test = %+v", test)
	}

	if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/model-aliases", nil, `{"rules":"oops"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("格式不对的规则应返回 400，实际 %d", resp.StatusCode)
	}

	svc.Close(t.Context()) // 写完排队的用量记录
	var logs struct {
		Records []struct {
			RequestModel  string `json:"request_model"`
			ResolvedModel string `json:"resolved_model"`
			Protocol      string `json:"protocol"`
		} `json:"records"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/logs?protocol=anthropic", nil, ""), &logs)
	if len(logs.Records) != 1 || logs.Records[0].RequestModel != "claude-haiku-5-5-20261001" ||
		logs.Records[0].ResolvedModel != "gemini-3-flash-high" || logs.Records[0].Protocol != "anthropic" {
		t.Fatalf("usage = %+v", logs.Records)
	}
}

// Anthropic 兼容默认开：客户端 system 被降为开头的用户消息（上游只剩这条，mock 回显它）。
// 关掉后 system 回到 systemInstruction，mock 回显真正的用户消息。
func TestMessagesSystemAsUser(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := anthropicHeaders(t, srv)

	read := func() string {
		resp := doReq(t, srv, http.MethodPost, "/v1/messages", h,
			`{"model":"gemini-3-flash","max_tokens":50,"system":"You are Claude Code.","messages":[{"role":"user","content":"hello there"}]}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, readBody(resp))
		}
		var msg struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		decode(t, resp, &msg)
		if len(msg.Content) != 1 {
			t.Fatalf("content = %+v", msg.Content)
		}
		return msg.Content[0].Text
	}

	// 默认开：mock 回显第一条用户文本，即被移到前面的说明。
	if got := read(); !strings.Contains(got, "client application's own system instructions") {
		t.Fatalf("兼容默认开时 system 应降为开头用户消息，mock 回显 = %q", got)
	}
	// 关掉：system 回到 systemInstruction，回显真正的用户消息。
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/anthropic-compat", nil, `{"system_as_user":false}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("save compat: %d %s", resp.StatusCode, readBody(resp))
	}
	if got := read(); got != "echo: hello there" {
		t.Fatalf("兼容关闭时应回显真正的用户消息，实际 %q", got)
	}
}
