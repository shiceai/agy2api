package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/shiceai/agy2api/internal/mockupstream"
	"github.com/shiceai/agy2api/internal/relay"
	"github.com/shiceai/agy2api/internal/store"
)

type importResponse struct {
	Account struct {
		ID     int64  `json:"id"`
		Email  string `json:"email"`
		Status string `json:"status"`
	} `json:"account"`
	Created   bool `json:"created"`
	Reenabled bool `json:"reenabled"`
}

func postImport(t *testing.T, srv *httptest.Server, path, body string) importResponse {
	t.Helper()
	resp := doReq(t, srv, http.MethodPost, path, nil, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: status %d body %s", path, resp.StatusCode, readBody(resp))
	}
	var out importResponse
	decode(t, resp, &out)
	return out
}

// 重复导入同一邮箱：换上新凭据，沿用设备画像；refresh token 失效停用的重新启用，手动停用的保持停用。
func TestImportUpdatesExistingAccount(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	ctx := context.Background()

	first := postImport(t, srv, "/api/admin/accounts/import", `{"refresh_token":"rt-1"}`)
	if !first.Created || first.Account.Status != store.StatusEnabled {
		t.Fatalf("first import: %+v", first)
	}
	id := first.Account.ID

	// 模拟 refresh token 失效被自动停用；改一个设备画像，确认更新时沿用。
	a, err := st.GetAccount(ctx, svc.enc, id)
	if err != nil {
		t.Fatal(err)
	}
	a.UserAgent = "antigravity/hub/2.31.0 (aidev_client; os_type=linux; arch=amd64; cl=1)"
	if err := st.UpdateAccountSync(ctx, svc.enc, a); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountStatus(ctx, id, store.StatusDisabled, relay.ReasonInvalidGrant); err != nil {
		t.Fatal(err)
	}

	second := postImport(t, srv, "/api/admin/accounts/import", `{"refresh_token":"rt-2"}`)
	if second.Created || !second.Reenabled || second.Account.ID != id || second.Account.Status != store.StatusEnabled {
		t.Fatalf("second import: %+v", second)
	}
	a, err = st.GetAccount(ctx, svc.enc, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.RefreshToken != "rt-2" || a.UserAgent != "antigravity/hub/2.31.0 (aidev_client; os_type=linux; arch=amd64; cl=1)" || a.DisabledReason != "" {
		t.Fatalf("updated account: token %q ua %q reason %q", a.RefreshToken, a.UserAgent, a.DisabledReason)
	}

	// 手动停用的不因重新导入而启用。
	if err := st.SetAccountStatus(ctx, id, store.StatusDisabled, ""); err != nil {
		t.Fatal(err)
	}
	third := postImport(t, srv, "/api/admin/accounts/import", `{"refresh_token":"rt-3"}`)
	if third.Reenabled || third.Account.Status != store.StatusDisabled {
		t.Fatalf("third import: %+v", third)
	}
	list, _ := st.ListAccounts(ctx, svc.enc)
	if len(list) != 1 {
		t.Fatalf("want 1 account, got %d", len(list))
	}
}

func TestOAuthLogin(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	start := func() (id, state, redirect string) {
		t.Helper()
		resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/oauth/start", nil, "")
		var out struct {
			SessionID string `json:"session_id"`
			AuthURL   string `json:"auth_url"`
		}
		decode(t, resp, &out)
		u, err := url.Parse(out.AuthURL)
		if err != nil || out.SessionID == "" {
			t.Fatalf("start: %+v %v", out, err)
		}
		return out.SessionID, u.Query().Get("state"), u.Query().Get("redirect_uri")
	}
	complete := func(id, callback string) *http.Response {
		body, _ := json.Marshal(map[string]string{"session_id": id, "callback_url": callback})
		return doReq(t, srv, http.MethodPost, "/api/admin/accounts/oauth/complete", nil, string(body))
	}

	id, state, redirect := start()
	if u, _ := url.Parse(redirect); u.Hostname() != "127.0.0.1" || u.Path != "/oauth-callback" || u.Port() == "" {
		t.Fatalf("redirect: %s", redirect)
	}
	// state 对不上：拒绝，会话保留，可以重新粘贴。
	if resp := complete(id, redirect+"?state=other&code=c1"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong state: %d %s", resp.StatusCode, readBody(resp))
	}
	// 只粘贴了问号后面的部分也行。
	resp := complete(id, "?state="+url.QueryEscape(state)+"&code=c1&scope=openid")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete: %d %s", resp.StatusCode, readBody(resp))
	}
	var out importResponse
	decode(t, resp, &out)
	if !out.Created || out.Account.Email != "mock-user@example.com" {
		t.Fatalf("complete result: %+v", out)
	}
	a, err := st.GetAccount(context.Background(), svc.enc, out.Account.ID)
	if err != nil || a.RefreshToken != "mock-refresh-from-code" {
		t.Fatalf("stored token: %v %q", err, a.RefreshToken)
	}
	// 会话只能用一次。
	if resp := complete(id, redirect+"?state="+url.QueryEscape(state)+"&code=c1"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reuse: %d", resp.StatusCode)
	}

	// 用户取消授权：报错，会话作废。
	id, state, redirect = start()
	if resp := complete(id, redirect+"?error=access_denied&state="+url.QueryEscape(state)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("denied: %d", resp.StatusCode)
	}
	if resp := complete(id, redirect+"?state="+url.QueryEscape(state)+"&code=c2"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("after denied: %d", resp.StatusCode)
	}
}

func TestBatchAccounts(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	ctx := context.Background()
	var ids []int64
	for _, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		a := &store.Account{Email: email, RefreshToken: "rt-" + email, ProjectID: "p"}
		if err := st.CreateAccount(ctx, svc.enc, a); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	if err := st.SetAccountStatus(ctx, ids[0], store.StatusDisabled, relay.ReasonInvalidGrant); err != nil {
		t.Fatal(err)
	}
	batch := func(action string, ids []int64) (int, int64) {
		body, _ := json.Marshal(map[string]any{"action": action, "ids": ids})
		resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/batch", nil, string(body))
		if resp.StatusCode != http.StatusOK {
			return resp.StatusCode, 0
		}
		var out struct {
			Affected int64 `json:"affected"`
		}
		decode(t, resp, &out)
		return resp.StatusCode, out.Affected
	}
	status := func(id int64) (string, string) {
		a, err := st.GetAccount(ctx, svc.enc, id)
		if err != nil {
			t.Fatal(err)
		}
		return a.Status, a.DisabledReason
	}

	if code, n := batch("disable", ids[1:]); code != http.StatusOK || n != 2 {
		t.Fatalf("disable: %d %d", code, n)
	}
	if s, _ := status(ids[2]); s != store.StatusDisabled {
		t.Fatalf("c status %s", s)
	}
	// 启用时清掉停用原因。
	if code, n := batch("enable", ids[:2]); code != http.StatusOK || n != 2 {
		t.Fatalf("enable: %d %d", code, n)
	}
	if s, reason := status(ids[0]); s != store.StatusEnabled || reason != "" {
		t.Fatalf("a after enable: %s %q", s, reason)
	}
	if code, n := batch("delete", []int64{ids[0], ids[2], 9999}); code != http.StatusOK || n != 2 {
		t.Fatalf("delete: %d %d", code, n)
	}
	if list, _ := st.ListAccounts(ctx, svc.enc); len(list) != 1 || list[0].ID != ids[1] {
		t.Fatalf("remaining: %v", list)
	}
	if code, _ := batch("sync", ids[1:2]); code != http.StatusBadRequest {
		t.Fatalf("unknown action: %d", code)
	}
	if code, _ := batch("enable", nil); code != http.StatusBadRequest {
		t.Fatalf("empty ids: %d", code)
	}
}

func TestTestAccountEndpoint(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	id := postImport(t, srv, "/api/admin/accounts/import", `{"refresh_token":"rt-1"}`).Account.ID
	path := "/api/admin/accounts/" + strconv.FormatInt(id, 10) + "/test"

	resp := doReq(t, srv, http.MethodPost, path, nil, `{"model":"gemini-3-pro-high"}`)
	var res struct {
		OK     bool   `json:"ok"`
		Status int    `json:"status"`
		Reply  string `json:"reply"`
	}
	decode(t, resp, &res)
	if !res.OK || res.Status != http.StatusOK || res.Reply == "" {
		t.Fatalf("probe: %+v", res)
	}
	if resp := doReq(t, srv, http.MethodPost, path, nil, `{"model":"not-in-catalog"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown model: %d", resp.StatusCode)
	}
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/999/test", nil, `{"model":"gemini-3-pro-high"}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown account: %d", resp.StatusCode)
	}
}

// 手动同步：成功返回账号；refresh token 已失效时返回中文原因和 code=invalid_grant，并停用账号，原因和请求链路一致。
func TestSyncAccount(t *testing.T) {
	mock := mockupstream.Handler()
	svc, st := newTestServiceWith(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" && r.PostFormValue("refresh_token") == "rt-revoked" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Bad Request"}`))
			return
		}
		mock.ServeHTTP(w, r)
	}))
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	ctx := context.Background()
	id := postImport(t, srv, "/api/admin/accounts/import", `{"refresh_token":"rt-1"}`).Account.ID
	path := "/api/admin/accounts/" + strconv.FormatInt(id, 10) + "/sync"

	if resp := doReq(t, srv, http.MethodPost, path, nil, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("sync: %d %s", resp.StatusCode, readBody(resp))
	}

	a, err := st.GetAccount(ctx, svc.enc, id)
	if err != nil {
		t.Fatal(err)
	}
	a.RefreshToken = "rt-revoked"
	if err := st.UpdateAccountSync(ctx, svc.enc, a); err != nil {
		t.Fatal(err)
	}
	resp := doReq(t, srv, http.MethodPost, path, nil, "")
	var out struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	decode(t, resp, &out)
	if resp.StatusCode != http.StatusBadRequest || out.Code != "invalid_grant" || !strings.HasPrefix(out.Error, relay.ReasonInvalidGrant) {
		t.Fatalf("revoked sync: %d %+v", resp.StatusCode, out)
	}
	if a, err = st.GetAccount(ctx, svc.enc, id); err != nil {
		t.Fatal(err)
	}
	if a.Status != store.StatusDisabled || a.DisabledReason != relay.ReasonInvalidGrant {
		t.Fatalf("after revoked sync: %s %q", a.Status, a.DisabledReason)
	}
}
