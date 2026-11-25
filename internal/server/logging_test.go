package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/logx"
)

// syncBuffer 是并发安全的日志缓冲：服务端 goroutine 写，测试读。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs 把默认日志器换成写入缓冲的 debug 级日志器，测试结束后还原。
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(logx.New(buf, slog.LevelDebug))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// 请求 ID 贯穿响应头、选号与上游调用日志、用量记录；日志里不出现 token 和 API Key。
func TestRequestIDAndNoSecretsInLogs(t *testing.T) {
	logs := captureLogs(t)
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	const refreshToken = "mock-refresh-token"
	resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/import", nil, `{"refresh_token":"`+refreshToken+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import account: status %d body %s", resp.StatusCode, readBody(resp))
	}
	resp = doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"log-test"}`)
	var keyResp struct {
		Secret string `json:"secret"`
	}
	decode(t, resp, &keyResp)
	auth := map[string]string{"x-goog-api-key": keyResp.Secret}

	genBody := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
	resp = doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent", auth, genBody)
	genID := resp.Header.Get("X-Request-Id")
	readBody(resp)
	if len(genID) != 16 {
		t.Fatalf("响应头应带 16 位 X-Request-Id，实际 %q", genID)
	}
	resp = doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:streamGenerateContent?alt=sse", auth, genBody)
	streamID := resp.Header.Get("X-Request-Id")
	readBody(resp)
	if streamID == "" || streamID == genID {
		t.Fatalf("每个请求应有独立的请求 ID：%q %q", genID, streamID)
	}
	// 查询串里的密钥不能进访问日志。
	resp = doReq(t, srv, http.MethodGet, "/v1beta/models?key="+keyResp.Secret, auth, "")
	readBody(resp)

	// 用量记录带上对应的请求 ID（异步写入，稍等）。
	want := map[string]bool{genID: false, streamID: false}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		records, err := st.RecentUsage(context.Background(), 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range records {
			if _, ok := want[r.RequestID]; ok {
				want[r.RequestID] = true
			}
		}
		if want[genID] && want[streamID] {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !want[genID] || !want[streamID] {
		t.Fatalf("用量记录缺少请求 ID：%v", want)
	}

	out := logs.String()
	for _, msg := range []string{"选号", "上游响应", "请求完成"} {
		if !hasLogLine(out, msg, "request_id="+genID) {
			t.Errorf("日志 %q 应带 request_id=%s", msg, genID)
		}
	}
	if !hasLogLine(out, "已导入账号", "request_id=") {
		t.Errorf("管理端日志应带 request_id")
	}
	secrets := map[string]string{
		"API Key":       keyResp.Secret,
		"refresh token": refreshToken,
		"access token":  "mock-access-", // mock 签发的 access token 前缀
	}
	for name, secret := range secrets {
		if strings.Contains(out, secret) {
			t.Errorf("日志里出现了 %s", name)
		}
	}
	if t.Failed() {
		t.Logf("日志：\n%s", out)
	}
}

// hasLogLine 判断是否有一行日志同时包含 msg 和 attr。
func hasLogLine(logs, msg, attr string) bool {
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "msg="+msg) && strings.Contains(line, attr) {
			return true
		}
	}
	return false
}
