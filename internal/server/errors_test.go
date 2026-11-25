package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shiceai/agy2api/internal/mockupstream"
)

// 生成失败时客户端只拿到通用提示和请求 ID，拿不到上游地址等内部细节。
func TestGenerateFailureHidesInternalError(t *testing.T) {
	mock := mockupstream.Handler()
	// 生成接口一律断开连接，其它接口（导入账号用的控制面）照常。
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "enerateContent") {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		mock.ServeHTTP(w, r)
	})
	svc, _ := newTestServiceWith(t, upstream)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/import", nil, `{"refresh_token":"mock-refresh-token"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import account: status %d body %s", resp.StatusCode, readBody(resp))
	}
	resp = doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"err-test"}`)
	var key struct {
		Secret string `json:"secret"`
	}
	decode(t, resp, &key)

	resp = doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent",
		map[string]string{"x-goog-api-key": key.Secret}, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	reqID := resp.Header.Get("X-Request-Id")
	body := readBody(resp)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("上游连不上应返回 503，实际 %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "request id: "+reqID) {
		t.Fatalf("错误信息应带请求 ID %s：%s", reqID, body)
	}
	for _, leak := range []string{"127.0.0.1", "v1internal", "EOF", "Post "} {
		if strings.Contains(body, leak) {
			t.Fatalf("错误信息泄露了内部细节 %q：%s", leak, body)
		}
	}
}

// 请求体超限返回 413，不静默截断；管理端非法 JSON 返回 400，空请求体按 {} 处理。
func TestRequestBodyLimits(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	resp := doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, "")
	var key struct {
		Secret string `json:"secret"`
	}
	decode(t, resp, &key)
	if key.Secret == "" {
		t.Fatal("空请求体应能创建 Key")
	}
	auth := map[string]string{"x-goog-api-key": key.Secret}

	huge := `{"contents":[{"role":"user","parts":[{"text":"` + strings.Repeat("a", inboundBodyLimit) + `"}]}]}`
	cases := []struct {
		name    string
		path    string
		headers map[string]string
		body    string
		want    int
	}{
		{"生成请求超过 16 MiB", "/v1beta/models/gemini-3-pro-high:generateContent", auth, huge, http.StatusRequestEntityTooLarge},
		{"计数请求超过 16 MiB", "/v1beta/models/gemini-3-pro-high:countTokens", auth, huge, http.StatusRequestEntityTooLarge},
		{"导入账号超过 1 MiB", "/api/admin/accounts/import", nil, `{"refresh_token":"` + strings.Repeat("a", 1<<20) + `"}`, http.StatusRequestEntityTooLarge},
		{"创建 Key 非法 JSON", "/api/admin/keys", nil, `{"name":`, http.StatusBadRequest},
		{"导入账号非法 JSON", "/api/admin/accounts/import", nil, `not json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		resp := doReq(t, srv, http.MethodPost, tc.path, tc.headers, tc.body)
		body := readBody(resp)
		if resp.StatusCode != tc.want {
			t.Errorf("%s：期望 %d，实际 %d %.200s", tc.name, tc.want, resp.StatusCode, body)
		}
	}
}

// 概览接口查库出错时返回 500，不把错误吞掉显示成 0。
func TestOverviewReportsStoreError(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	if resp := doReq(t, srv, http.MethodGet, "/api/admin/overview", nil, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("正常时应返回 200，实际 %d", resp.StatusCode)
	}
	st.Close()
	resp := doReq(t, srv, http.MethodGet, "/api/admin/overview", nil, "")
	body := readBody(resp)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("查库出错时应返回 500，实际 %d %s", resp.StatusCode, body)
	}
}

// 前端用 History 模式，未匹配的路径回退到 index.html；接口路径不回退，返回 JSON 404。
func TestUnknownAPIPathsReturnJSON(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	for _, path := range []string{"/v1beta/nope", "/v1/nope", "/api/nope"} {
		resp := doReq(t, srv, http.MethodGet, path, nil, "")
		body := readBody(resp)
		if resp.StatusCode != http.StatusNotFound || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("%s: %d %s %s", path, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
	}
}
