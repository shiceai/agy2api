package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/mockupstream"
	"github.com/shiceai/agy2api/internal/store"
)

// 假流式：上游用非流式请求（这里的上游只在 generateContent 上可调延迟和失败），结果按一个 SSE 事件返回。
func TestFakeStream(t *testing.T) {
	prev := fakeStreamWait
	fakeStreamWait = 100 * time.Millisecond
	t.Cleanup(func() { fakeStreamWait = prev })

	var delay atomic.Int64 // 毫秒
	var failStatus atomic.Int32
	var streamCalls atomic.Int32
	mock := mockupstream.Handler()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1internal:generateContent", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(delay.Load()) * time.Millisecond)
		if code := failStatus.Load(); code != 0 {
			w.WriteHeader(int(code))
			_, _ = w.Write([]byte(`{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`))
			return
		}
		mock.ServeHTTP(w, r)
	})
	mux.HandleFunc("/v1internal:streamGenerateContent", func(w http.ResponseWriter, r *http.Request) {
		streamCalls.Add(1)
		mock.ServeHTTP(w, r)
	})
	mux.Handle("/", mock)
	svc, st := newTestServiceWith(t, mux)
	p := svc.relay.Policy()
	p.CooldownWait = 0 // 只有一个账号，5xx 后不等它休息完，直接失败
	svc.relay.SetPolicy(p)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	auth := importAndKey(t, srv)
	body := `{"contents":[{"role":"user","parts":[{"text":"hello there"}]}]}`
	fake := "/v1beta/models/假流式/gemini-3-pro-high:streamGenerateContent?alt=sse"
	dataEvents := func(s string) []map[string]any {
		var out []map[string]any
		for _, line := range strings.Split(s, "\n") {
			if rest, ok := strings.CutPrefix(line, "data: "); ok {
				var m map[string]any
				if err := json.Unmarshal([]byte(rest), &m); err != nil {
					t.Fatalf("bad data line %q: %v", line, err)
				}
				out = append(out, m)
			}
		}
		return out
	}

	// 很快有结果：直接一个 data 事件，没有保活注释；上游走的是非流式。
	resp := doReq(t, srv, http.MethodPost, fake, auth, body)
	got := readBody(resp)
	events := dataEvents(got)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") ||
		len(events) != 1 || events[0]["candidates"] == nil || events[0]["usageMetadata"] == nil || strings.Contains(got, "keepalive") {
		t.Fatalf("fast fake stream: %d %q", resp.StatusCode, got)
	}
	if streamCalls.Load() != 0 {
		t.Fatal("fake stream must call upstream non-stream")
	}
	// 客户端把 / 编码成 %2F 也认。
	if resp := doReq(t, srv, http.MethodPost, "/v1beta/models/%E5%81%87%E6%B5%81%E5%BC%8F%2Fgemini-3-pro-high:streamGenerateContent", auth, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("encoded slash: %d %s", resp.StatusCode, readBody(resp))
	}

	// 慢：先回 200，等待期间发保活注释，最后是结果。
	delay.Store(350)
	resp = doReq(t, srv, http.MethodPost, fake, auth, body)
	got = readBody(resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(got, ": keepalive\n\n") || len(dataEvents(got)) != 1 || dataEvents(got)[0]["candidates"] == nil {
		t.Fatalf("slow fake stream: %d %q", resp.StatusCode, got)
	}

	// 慢且失败：200 之后发错误事件。
	failStatus.Store(http.StatusInternalServerError)
	resp = doReq(t, srv, http.MethodPost, fake, auth, body)
	got = readBody(resp)
	if ev := dataEvents(got); resp.StatusCode != http.StatusOK || len(ev) != 1 || ev[0]["error"] == nil {
		t.Fatalf("slow failing fake stream: %d %q", resp.StatusCode, got)
	}

	// 很快失败：返回正常的错误状态码。
	delay.Store(0)
	failStatus.Store(0)
	if resp := doReq(t, srv, http.MethodPost, "/v1beta/models/假流式/no-such-model:streamGenerateContent", auth, body); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("fast failing fake stream: %d", resp.StatusCode)
	}

	// 用量：模型名记去掉前缀的，stream 为 false，详情里标假流式。
	for i := 0; i < 100; i++ {
		recs, _ := st.RecentUsage(context.Background(), 20)
		n := 0
		for _, r := range recs {
			var d store.UsageDetail
			_ = json.Unmarshal(r.Detail, &d)
			if d.FakeStream {
				n++
				if r.Stream || strings.HasPrefix(r.RequestModel, fakeStreamPrefix) {
					t.Fatalf("fake stream record: %+v", r)
				}
			}
		}
		if n == 5 {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("fake stream usage records missing")
}
