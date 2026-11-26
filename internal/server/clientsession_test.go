package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shiceai/agy2api/internal/store"
)

func TestClientSession(t *testing.T) {
	cases := []struct {
		headers     map[string]string
		id, source  string
		explanation string
	}{
		{nil, "", "", "没有会话头"},
		{map[string]string{"X-Claude-Code-Session-Id": " s1 "}, "s1", store.SessionClaudeCode, "Claude Code 主会话"},
		{map[string]string{"X-Claude-Code-Session-Id": "s1", "X-Claude-Code-Agent-Id": "a1"}, "s1/a1", store.SessionClaudeCodeAgent, "子代理单独认"},
		{map[string]string{"X-Session-Id": "user-7"}, "user-7", store.SessionHeader, "通用会话头"},
		{map[string]string{"X-Session-Id": "user-7", "X-Claude-Code-Session-Id": "s1"}, "s1", store.SessionClaudeCode, "Claude Code 的会话头优先"},
		{map[string]string{"X-Claude-Code-Agent-Id": "a1"}, "", "", "只有 agent ID 不算"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		if id, source := clientSession(r); id != c.id || source != c.source {
			t.Errorf("%s：得到 %q %q，应为 %q %q", c.explanation, id, source, c.id, c.source)
		}
	}
}

// 端到端：用量详情的会话一节记下按什么认的对话。Claude Code 的会话头和子代理、OpenAI 的 prompt_cache_key
// （会话头优先）、都没有时按首条用户消息。
func TestSessionSourceInUsage(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	ah := anthropicHeaders(t, srv)
	oh := map[string]string{"Authorization": "Bearer " + ah["x-api-key"]}
	with := func(h map[string]string, extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range h {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	messages := `{"model":"gemini-3-flash","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	chat := func(extra string) string {
		return `{"model":"gemini-3-flash",` + extra + `"messages":[{"role":"user","content":"hi"}]}`
	}
	reqs := []struct {
		path, body string
		headers    map[string]string
	}{
		{"/v1/messages", messages, with(ah, map[string]string{"X-Claude-Code-Session-Id": "cc-1"})},
		{"/v1/messages", messages, with(ah, map[string]string{"X-Claude-Code-Session-Id": "cc-1", "X-Claude-Code-Agent-Id": "ag-1"})},
		{"/v1/chat/completions", chat(`"prompt_cache_key":"pck-1",`), oh},
		{"/v1/chat/completions", chat(`"prompt_cache_key":"pck-1",`), with(oh, map[string]string{"X-Session-Id": "u-1"})},
		{"/v1/chat/completions", chat(""), oh},
	}
	for _, q := range reqs {
		resp := doReq(t, srv, http.MethodPost, q.path, q.headers, q.body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", q.path, resp.StatusCode, readBody(resp))
		}
		_ = readBody(resp)
	}
	svc.Close(t.Context()) // 写完排队的用量记录

	var logs struct {
		Records []struct {
			ID int64 `json:"id"`
		} `json:"records"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/logs", nil, ""), &logs)
	if len(logs.Records) != len(reqs) {
		t.Fatalf("records = %d", len(logs.Records))
	}
	// 列表按时间倒序。
	want := []string{store.SessionFirstMessage, store.SessionHeader, store.SessionPromptCacheKey, store.SessionClaudeCodeAgent, store.SessionClaudeCode}
	for i, rec := range logs.Records {
		var one struct {
			Record struct {
				Detail store.UsageDetail `json:"detail"`
			} `json:"record"`
		}
		decode(t, doReq(t, srv, http.MethodGet, fmt.Sprintf("/api/admin/usage/logs/%d", rec.ID), nil, ""), &one)
		a := one.Record.Detail.Affinity
		if a == nil || a.Source != want[i] || a.Result != "new" {
			t.Fatalf("record %d: affinity = %+v, want source %s", i, a, want[i])
		}
	}
}
