package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// importAndKey 导入 mock 账号并创建 API Key，返回带 Key 的请求头。
func importAndKey(t *testing.T, srv *httptest.Server) map[string]string {
	t.Helper()
	resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/import", nil, `{"refresh_token":"mock-refresh-token"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import account: status %d body %s", resp.StatusCode, readBody(resp))
	}
	resp = doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"test"}`)
	var key struct {
		Secret string `json:"secret"`
	}
	decode(t, resp, &key)
	return map[string]string{"x-goog-api-key": key.Secret}
}

// 家族名按目录解析（mock 目录有 gemini-3-pro-low 和 -high，默认档位 medium 缺失时先往低取 low）；
// 目录里没有的模型返回 404。
func TestModelResolutionOverHTTP(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	auth := importAndKey(t, srv)

	resp := doReq(t, srv, http.MethodGet, "/v1beta/models/gemini-3-pro", auth, "")
	var record struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}
	decode(t, resp, &record)
	if record.Name != "models/gemini-3-pro" || record.DisplayName != "Gemini 3 Pro (Low)" {
		t.Fatalf("家族名应解析到 gemini-3-pro-low，实际 %+v", record)
	}

	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/v1beta/models/gemini-9", http.StatusNotFound},
		{http.MethodPost, "/v1beta/models/gemini-3-pro:generateContent", http.StatusOK},
		{http.MethodPost, "/v1beta/models/gemini-9:generateContent", http.StatusNotFound},
		{http.MethodPost, "/v1beta/models/gemini-9:streamGenerateContent?alt=sse", http.StatusNotFound},
		{http.MethodPost, "/v1beta/models/gemini-9:countTokens", http.StatusNotFound},
	}
	for _, tc := range cases {
		reqBody := body
		if tc.method == http.MethodGet {
			reqBody = ""
		}
		resp := doReq(t, srv, tc.method, tc.path, auth, reqBody)
		out := readBody(resp)
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s：期望 %d，实际 %d %s", tc.method, tc.path, tc.want, resp.StatusCode, out)
		}
	}
}

// Gemini 入站兼容：/v1 是 /v1beta 的别名；没有请求头时接受 ?key=，有请求头时以请求头为准；
// ?key= 里的 Key 不写进日志。
func TestGeminiCompat(t *testing.T) {
	logs := captureLogs(t)
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	key := importAndKey(t, srv)["x-goog-api-key"]
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`

	if resp := doReq(t, srv, http.MethodGet, "/v1/models?key="+key, nil, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/models?key=: %d", resp.StatusCode)
	}
	if resp := doReq(t, srv, http.MethodPost, "/v1/models/gemini-3-pro-high:generateContent?key="+key, nil, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1 generate: %d %s", resp.StatusCode, readBody(resp))
	}
	if resp := doReq(t, srv, http.MethodGet, "/v1beta/models/gemini-3-pro-high?key="+key, nil, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("get model with ?key=: %d", resp.StatusCode)
	}
	// 请求头优先：请求头里的 Key 无效时不看查询串。
	if resp := doReq(t, srv, http.MethodGet, "/v1beta/models?key="+key, map[string]string{"x-goog-api-key": "bad"}, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("header should take precedence: %d", resp.StatusCode)
	}
	if strings.Contains(logs.String(), key) {
		t.Fatal("API key from ?key= leaked into logs")
	}
}

// 模型列表按官方客户端的口径：选择器和辅助用途里有的模型，分档的合成家族名（显示名用家族的）；带档位的名字
// 不列出但照样能调用。Key 只允许了家族里某一档时列这一档。
func TestModelListFamilies(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	auth := importAndKey(t, srv)

	var list struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/v1beta/models", auth, ""), &list)
	got := fmt.Sprint(list.Models)
	if got != "[{models/gemini-3-flash-high Gemini 3 Flash (High)} {models/gemini-3-pro Gemini 3 Pro}]" {
		t.Fatalf("models = %s", got)
	}
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	if resp := doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent", auth, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("不列出的档位应照样能调用：%d %s", resp.StatusCode, readBody(resp))
	}

	resp := doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"tier","allowed_models":["gemini-3-pro-high"]}`)
	var key struct {
		Secret string `json:"secret"`
	}
	decode(t, resp, &key)
	decode(t, doReq(t, srv, http.MethodGet, "/v1beta/models", map[string]string{"x-goog-api-key": key.Secret}, ""), &list)
	if got := fmt.Sprint(list.Models); got != "[{models/gemini-3-pro-high Gemini 3 Pro (High)}]" {
		t.Fatalf("只允许一档时 models = %s", got)
	}
}
