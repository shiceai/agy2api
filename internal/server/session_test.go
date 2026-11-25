package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// /api/admin/session：口令对时返回版本信息（登录页当场校验、管理端显示版本），口令不对或没带时 401。
func TestAdminSession(t *testing.T) {
	svc, _ := newTestService(t)
	svc.cfg.AllowNoAdminAuth = false
	svc.cfg.AdminPassword = "secret"
	svc.build = readBuildInfo("v0.1.0-3-gabc1234")
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	var got buildInfo
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/session", map[string]string{"X-Admin-Password": "secret"}, ""), &got)
	if got.Version != "v0.1.0-3-gabc1234" {
		t.Fatalf("应返回构建注入的版本号，实际 %+v", got)
	}
	for _, pw := range []string{"wrong", ""} {
		headers := map[string]string{}
		if pw != "" {
			headers["X-Admin-Password"] = pw
		}
		if resp := doReq(t, srv, http.MethodGet, "/api/admin/session", headers, ""); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("口令 %q 应返回 401，实际 %d", pw, resp.StatusCode)
		}
	}
	if readBuildInfo("").Version != "dev" {
		t.Fatal("没有注入版本号时应为 dev")
	}
}
