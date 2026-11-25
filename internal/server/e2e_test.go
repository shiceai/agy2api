package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/config"
	"github.com/shiceai/agy2api/internal/cryptox"
	"github.com/shiceai/agy2api/internal/mockupstream"
	"github.com/shiceai/agy2api/internal/store"
)

// newTestService 用 mock 上游和临时数据库构造一个服务。
func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	return newTestServiceWith(t, mockupstream.Handler())
}

// newTestServiceWith 用给定的上游 handler 和临时数据库构造一个服务，opts 可以改配置。
func newTestServiceWith(t *testing.T, upstream http.Handler, opts ...func(*config.Config)) (*Service, *store.Store) {
	t.Helper()
	mock := httptest.NewServer(upstream)
	t.Cleanup(mock.Close)

	cfg := config.Config{
		DataDir:          t.TempDir(),
		AllowNoAdminAuth: true,
		UpstreamBases:    []string{mock.URL},
		TokenURL:         mock.URL + "/token",
		UserInfoURL:      mock.URL + "/oauth2/v2/userinfo",
		UserAgent:        "antigravity/hub/test linux/amd64",
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	enc, err := cryptox.New([]byte("test-key-material"))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := NewService(context.Background(), cfg, st, enc)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	t.Cleanup(func() { svc.Close(context.Background()) })
	return svc, st
}

func TestEndToEndGemini(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	ctx := context.Background()

	// 导入账号（走 mock 的 OAuth/loadCodeAssist/fetchAvailableModels）。
	importBody := `{"refresh_token":"mock-refresh-token"}`
	resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/import", nil, importBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import account: status %d body %s", resp.StatusCode, readBody(resp))
	}

	// 创建 API Key，拿明文。
	resp = doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"test"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create key: status %d", resp.StatusCode)
	}
	var keyResp struct {
		Secret string `json:"secret"`
	}
	decode(t, resp, &keyResp)
	if !strings.HasPrefix(keyResp.Secret, "agy-") {
		t.Fatalf("unexpected key secret: %q", keyResp.Secret)
	}

	authHeaders := map[string]string{"x-goog-api-key": keyResp.Secret}

	// 非流式 generateContent。
	genBody := `{"contents":[{"role":"user","parts":[{"text":"hello world"}]}]}`
	resp = doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent", authHeaders, genBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("generate: status %d body %s", resp.StatusCode, readBody(resp))
	}
	var gen map[string]any
	decode(t, resp, &gen)
	if text := firstCandidateText(gen); !strings.Contains(text, "echo: hello world") {
		t.Fatalf("unexpected generate text: %q (full: %v)", text, gen)
	}
	if _, ok := gen["usageMetadata"]; !ok {
		t.Fatalf("missing usageMetadata in response")
	}

	// 流式 streamGenerateContent。
	resp = doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:streamGenerateContent?alt=sse", authHeaders, genBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: status %d", resp.StatusCode)
	}
	streamBody := readBody(resp)
	if !strings.Contains(streamBody, "data: ") || !strings.Contains(streamBody, "echo: ") {
		t.Fatalf("unexpected stream body: %q", streamBody)
	}
	// 解包后不应再残留上游信封的 "response" 外层。
	if strings.Contains(streamBody, `"response"`) {
		t.Fatalf("stream frames not unwrapped: %q", streamBody)
	}

	// countTokens。
	resp = doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:countTokens", authHeaders, genBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("countTokens: status %d", resp.StatusCode)
	}

	// 列模型应能看到目录并集。
	resp = doReq(t, srv, http.MethodGet, "/v1beta/models", authHeaders, "")
	var listed map[string]any
	decode(t, resp, &listed)
	if models, _ := listed["models"].([]any); len(models) == 0 {
		t.Fatalf("expected models in list, got %v", listed)
	}

	// 用量记录应已落库（异步写，稍等）。
	time.Sleep(200 * time.Millisecond)
	records, err := st.RecentUsage(ctx, 10)
	if err != nil {
		t.Fatalf("recent usage: %v", err)
	}
	if len(records) == 0 {
		t.Fatalf("expected usage records")
	}
	foundModelVersion := false
	for _, r := range records {
		if r.UpstreamModel == "gemini-3-flash-high" {
			foundModelVersion = true
		}
	}
	if !foundModelVersion {
		t.Fatalf("expected a record carrying upstream modelVersion; got %+v", records)
	}
}

func TestMissingAPIKeyRejected(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	resp := doReq(t, srv, http.MethodGet, "/v1beta/models", nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", resp.StatusCode)
	}
}

// --- helpers ---

func doReq(t *testing.T, srv *httptest.Server, method, path string, headers map[string]string, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do request %s %s: %v", method, path, err)
	}
	return resp
}

func readBody(resp *http.Response) string {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}

func decode(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func firstCandidateText(resp map[string]any) string {
	candidates, _ := resp["candidates"].([]any)
	for _, c := range candidates {
		m, _ := c.(map[string]any)
		content, _ := m["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if t, ok := pm["text"].(string); ok {
				return t
			}
		}
	}
	return ""
}
