package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/mockupstream"
)

// hookServer 是记录收到的通知的假 Webhook。
type hookServer struct {
	mu     sync.Mutex
	bodies []map[string]any
	fail   atomic.Bool
}

func (h *hookServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.fail.Load() {
		http.Error(w, "nope", http.StatusForbidden)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var b map[string]any
	_ = json.Unmarshal(raw, &b)
	h.mu.Lock()
	h.bodies = append(h.bodies, b)
	h.mu.Unlock()
}

// titles 返回收到的通知标题。
func (h *hookServer) titles() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, b := range h.bodies {
		out = append(out, fmt.Sprint(b["title"]))
	}
	return out
}

// waitTitles 等到收到 n 条通知（最多 5 秒）。
func (h *hookServer) waitTitles(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.titles()) < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	return h.titles()
}

// 通知设置和渠道：默认值、校验；渠道的 Webhook 地址和密钥不回显，修改时不给就保持原值；发送测试成功和失败。
func TestNotifyAdmin(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	hook := &hookServer{}
	hookSrv := httptest.NewServer(hook)
	defer hookSrv.Close()

	var got struct {
		Settings struct {
			Events   map[string]bool `json:"events"`
			Instance string          `json:"instance"`
			Timezone string          `json:"timezone"`
		} `json:"settings"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/settings/notify", nil, ""), &got)
	if !got.Settings.Events["account_banned"] || got.Settings.Events["proxy_failed"] || got.Settings.Events["new_version"] {
		t.Fatalf("默认事件开关不对：%+v", got.Settings)
	}
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/notify", nil, `{"timezone":"Mars/Base"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("不认识的时区应返回 400：%d", resp.StatusCode)
	}
	decode(t, doReq(t, srv, http.MethodPut, "/api/admin/settings/notify", nil,
		`{"events":{"proxy_failed":true},"instance":"生产","timezone":"Asia/Shanghai"}`), &got)
	if !got.Settings.Events["proxy_failed"] || !got.Settings.Events["account_banned"] || got.Settings.Instance != "生产" {
		t.Fatalf("应只改给了的事件，其余保持：%+v", got.Settings)
	}

	type channel struct {
		ID        int64  `json:"id"`
		Hint      string `json:"hint"`
		HasSecret bool   `json:"has_secret"`
		Enabled   bool   `json:"enabled"`
		LastError string `json:"last_error"`
	}
	var created struct {
		Channel channel `json:"channel"`
	}
	hookURL := hookSrv.URL + "/hook?token=hook-secret-1234"
	resp := doReq(t, srv, http.MethodPost, "/api/admin/notify/channels", nil,
		`{"name":"运维群","type":"webhook","url":"`+hookURL+`","secret":"sign-secret"}`)
	raw := readBody(resp)
	if resp.StatusCode != http.StatusOK || strings.Contains(raw, "hook-secret") || strings.Contains(raw, "sign-secret") {
		t.Fatalf("新增渠道应成功且不回显凭据：%d %s", resp.StatusCode, raw)
	}
	_ = json.Unmarshal([]byte(raw), &created)
	if !created.Channel.HasSecret || !strings.HasSuffix(created.Channel.Hint, "/…1234") || !created.Channel.Enabled {
		t.Fatalf("渠道视图不对：%+v", created.Channel)
	}
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/notify/channels", nil, `{"name":"x","type":"webhook","url":"ftp://x"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("地址不对应返回 400：%d", resp.StatusCode)
	}
	path := fmt.Sprintf("/api/admin/notify/channels/%d", created.Channel.ID)
	var updated struct {
		Channel channel `json:"channel"`
	}
	decode(t, doReq(t, srv, http.MethodPut, path, nil, `{"enabled":false}`), &updated)
	if updated.Channel.Enabled || updated.Channel.Hint != created.Channel.Hint || !updated.Channel.HasSecret {
		t.Fatalf("只改启用状态时地址和密钥应保持：%+v", updated.Channel)
	}

	if resp := doReq(t, srv, http.MethodPost, path+"/test", nil, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("发送测试应成功（停用的渠道也能测）：%d %s", resp.StatusCode, readBody(resp))
	}
	if titles := hook.titles(); len(titles) != 1 || titles[0] != "测试消息" {
		t.Fatalf("应收到一条测试消息：%v", titles)
	}
	hook.fail.Store(true)
	resp = doReq(t, srv, http.MethodPost, path+"/test", nil, "")
	if msg := readBody(resp); resp.StatusCode != http.StatusBadGateway || !strings.Contains(msg, "HTTP 403") || strings.Contains(msg, "hook-secret") {
		t.Fatalf("发送失败应返回可读原因且不带地址：%d %s", resp.StatusCode, msg)
	}
	var list struct {
		Channels []channel `json:"channels"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/notify/channels", nil, ""), &list)
	if len(list.Channels) != 1 || !strings.Contains(list.Channels[0].LastError, "HTTP 403") {
		t.Fatalf("应记下最近一次失败：%+v", list.Channels)
	}
	if resp := doReq(t, srv, http.MethodDelete, path, nil, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("删除应成功：%d", resp.StatusCode)
	}
	if resp := doReq(t, srv, http.MethodDelete, path, nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("删除不存在的渠道应返回 404：%d", resp.StatusCode)
	}
}

// 封禁保护和告警：开启后账号被条款封禁时停用它、号池暂停，通知渠道收到封禁（邮箱打码、附申诉地址）和暂停两条；
// 暂停期间请求返回 503 和 Retry-After，实时接口带着暂停信息；管理端提前恢复后收到恢复通知。
func TestPoolPauseAndAlerts(t *testing.T) {
	mock := mockupstream.Handler()
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "enerateContent") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"This service has been disabled in this account for violation of Terms of Service.",`+
				`"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","metadata":{"appeal_url":"https://forms.gle/appeal"}}]}}`)
			return
		}
		mock.ServeHTTP(w, r)
	})
	svc, _ := newTestServiceWith(t, upstream)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	hook := &hookServer{}
	hookSrv := httptest.NewServer(hook)
	defer hookSrv.Close()

	var sched struct {
		Settings struct {
			TOSPause      bool `json:"tos_pause"`
			TOSPauseHours int  `json:"tos_pause_hours"`
		} `json:"settings"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/settings/scheduling", nil, ""), &sched)
	if sched.Settings.TOSPause || sched.Settings.TOSPauseHours != 6 {
		t.Fatalf("封禁保护默认应关、时长 6 小时：%+v", sched.Settings)
	}
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/scheduling", nil, `{"tos_pause":true,"tos_pause_hours":0}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("时长不在 1～72 小时应返回 400：%d", resp.StatusCode)
	}
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/scheduling", nil, `{"tos_pause":true,"tos_pause_hours":2}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("开启封禁保护失败：%d %s", resp.StatusCode, readBody(resp))
	}
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/notify/channels", nil, `{"name":"hook","type":"webhook","url":"`+hookSrv.URL+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("新增渠道失败：%d", resp.StatusCode)
	}
	postImport(t, srv, "/api/admin/accounts/import", `{"refresh_token":"rt-ban"}`)
	var key struct {
		Secret string `json:"secret"`
	}
	decode(t, doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"app"}`), &key)
	generate := func() *http.Response {
		return doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent", map[string]string{"x-goog-api-key": key.Secret}, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	}
	if resp := generate(); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("条款封禁应返回 503：%d", resp.StatusCode)
	}
	titles := hook.waitTitles(t, 2)
	if len(titles) != 2 || !contains(titles, "账号被条款封禁") || !contains(titles, "号池已暂停") {
		t.Fatalf("应收到封禁和暂停两条通知：%v", titles)
	}
	hook.mu.Lock()
	var bannedText string
	for _, b := range hook.bodies {
		if b["title"] == "账号被条款封禁" {
			bannedText = fmt.Sprint(b["text"])
		}
	}
	hook.mu.Unlock()
	if !strings.Contains(bannedText, "申诉：https://forms.gle/appeal") || !strings.Contains(bannedText, "m***@example.com") || strings.Contains(bannedText, "mock-user@") {
		t.Fatalf("封禁通知应附申诉地址、邮箱打码：%s", bannedText)
	}

	resp := generate()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("暂停中应返回 503 和 Retry-After：%d %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	var live struct {
		PoolPause *poolPauseView `json:"pool_pause"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/live", nil, ""), &live)
	if live.PoolPause == nil || live.PoolPause.Account == "" || time.Until(time.UnixMilli(live.PoolPause.Until)) < 119*time.Minute {
		t.Fatalf("实时接口应带着约 2 小时的暂停：%+v", live.PoolPause)
	}
	var resumed struct {
		Resumed bool `json:"resumed"`
	}
	decode(t, doReq(t, srv, http.MethodPost, "/api/admin/pool-pause/resume", nil, ""), &resumed)
	if !resumed.Resumed {
		t.Fatal("应提前恢复")
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/live", nil, ""), &live)
	if live.PoolPause != nil {
		t.Fatalf("恢复后不应再有暂停：%+v", live.PoolPause)
	}
	if titles := hook.waitTitles(t, 3); !contains(titles, "号池已恢复") {
		t.Fatalf("应收到恢复通知：%v", titles)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
