package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/shiceai/agy2api/internal/config"
	"github.com/shiceai/agy2api/internal/mockupstream"
	"github.com/shiceai/agy2api/internal/upgrade"
)

// 开发构建（测试里的 dev）不能查新版本、不能升级，接口给出原因。
func TestSystemDevBuild(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	var v systemView
	resp := doReq(t, srv, http.MethodGet, "/api/admin/system", nil, "")
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /system: %d, %v", resp.StatusCode, err)
	}
	if v.Version != "dev" || v.CanCheck || v.CanUpgrade || v.UnsupportedReason == "" {
		t.Fatalf("view = %+v", v)
	}
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/system/upgrade", nil, `{"version":"v9.9.9"}`); resp.StatusCode != http.StatusConflict {
		t.Fatalf("开发构建升级应返回 409，实际 %d", resp.StatusCode)
	}
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/system/revert", nil, ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("没有在线升级的程序时撤销应返回 409，实际 %d", resp.StatusCode)
	}
}

// 查到新版本后，版本页和侧边栏用的 session 都带上它；自动检查（auto=1）10 分钟内查过就不再连 GitHub。
func TestSystemCheckUpdate(t *testing.T) {
	var fetches atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+upgrade.Repo+"/releases/latest" {
			http.NotFound(w, r)
			return
		}
		fetches.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0","body":"- **新功能。**","html_url":"https://github.com/shiceai/agy2api/releases/tag/v0.2.0","assets":[]}`))
	}))
	defer gh.Close()
	svc, _ := newTestServiceWith(t, mockupstream.Handler(), func(c *config.Config) {
		c.Version, c.Update, c.UpdateAPI = "v0.1.0", true, gh.URL
	})
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	var v systemView
	resp := doReq(t, srv, http.MethodPost, "/api/admin/system/check?auto=1", nil, "")
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if !v.CanCheck || !v.UpdateAvailable || v.Latest == nil || v.Latest.Version != "v0.2.0" || v.CheckError != "" {
		t.Fatalf("view = %+v", v)
	}
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/system/check?auto=1", nil, ""); resp.StatusCode != http.StatusOK || fetches.Load() != 1 {
		t.Fatalf("刚查过时自动检查不应再连 GitHub：%d，查了 %d 次", resp.StatusCode, fetches.Load())
	}
	var s sessionView
	resp = doReq(t, srv, http.MethodGet, "/api/admin/session", nil, "")
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.Version != "v0.1.0" || s.Update != "v0.2.0" {
		t.Fatalf("session = %+v", s)
	}
}
