package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/mockupstream"
	"github.com/shiceai/agy2api/internal/pool"
	"github.com/shiceai/agy2api/internal/relay"
	"github.com/shiceai/agy2api/internal/store"
)

// scriptedUpstream 让生成接口返回给定的状态和错误体，其它接口（令牌、导入用的控制面）交给 mock。
func scriptedUpstream(status int, body string) (http.Handler, *atomic.Int32) {
	mock := mockupstream.Handler()
	var calls atomic.Int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "enerateContent") {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		mock.ServeHTTP(w, r)
	}), &calls
}

// 上游错误按分类改写后返回：429 带 Retry-After；403 改成 503 且不带上游说明；400 保留说明。
func TestUpstreamErrorsRewritten(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantHas    string
		leak       string // 不应出现在响应里的上游细节
		retryAfter bool
	}{
		{"额度耗尽", 429,
			`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached. Resets in 2h.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"2h"}}]}}`,
			429, "RESOURCE_EXHAUSTED", "Individual quota", true},
		{"条款封禁", 403,
			`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Gemini has been disabled in this account for violation of Terms of Service."}}`,
			503, "UNAVAILABLE", "Terms of Service", false},
		{"请求参数错误", 400,
			`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"contents is required","details":[{"@type":"x","metadata":{"project":"secret-project"}}]}}`,
			400, "contents is required", "secret-project", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream, _ := scriptedUpstream(tc.status, tc.body)
			svc, _ := newTestServiceWith(t, upstream)
			srv := httptest.NewServer(svc.Router())
			defer srv.Close()
			auth := importAndKey(t, srv)

			resp := doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent", auth,
				`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
			body := readBody(resp)
			if resp.StatusCode != tc.wantStatus || !strings.Contains(body, tc.wantHas) || strings.Contains(body, tc.leak) {
				t.Fatalf("期望 %d 含 %q 且不含 %q，实际 %d %s", tc.wantStatus, tc.wantHas, tc.leak, resp.StatusCode, body)
			}
			if ra := resp.Header.Get("Retry-After"); tc.retryAfter {
				if secs, err := strconv.Atoi(ra); err != nil || secs < 7100 || secs > 7200 {
					t.Fatalf("Retry-After 应约为 7200 秒，实际 %q", ra)
				}
			} else if ra != "" {
				t.Fatalf("不应带 Retry-After，实际 %q", ra)
			}

			// 条款封禁的账号已停用，账号列表里能看到原因；额度耗尽的账号模型在冷却。
			var list struct {
				Accounts []accountView `json:"accounts"`
			}
			decode(t, doReq(t, srv, http.MethodGet, "/api/admin/accounts", nil, ""), &list)
			a := list.Accounts[0]
			switch tc.name {
			case "条款封禁":
				if a.Status != store.StatusDisabled || !strings.Contains(a.DisabledReason, "TOS_VIOLATION") {
					t.Fatalf("账号应被停用并带原因：%+v", a)
				}
			case "额度耗尽":
				if len(a.Cooldowns) != 1 || a.Cooldowns[0].Model != "gemini-3-pro-high" || a.Cooldowns[0].Reason != "quota_exhausted" {
					t.Fatalf("账号列表应显示冷却：%+v", a.Cooldowns)
				}
			}
		})
	}
}

// 调度设置：GET 返回当前值和默认值；PUT 校验范围、落库、立即生效，缺省字段保持原值；重启后从库加载。
func TestSchedulingSettings(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	var got struct {
		Settings schedulingView `json:"settings"`
		Defaults schedulingView `json:"defaults"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/settings/scheduling", nil, ""), &got)
	if got.Settings != got.Defaults || got.Defaults.Concurrency != 15 || !got.Defaults.TokenRefreshEarly ||
		got.Defaults.TokenRefreshMinLead != 300 || got.Defaults.TokenRefreshMaxLead != 900 ||
		got.Defaults.QuotaRefresh != 900 || got.Defaults.LowQuota != 10 || got.Defaults.Strategy != pool.StrategyRoundRobin {
		t.Fatalf("未配置时应为默认值：%+v", got)
	}

	for _, bad := range []string{
		`{"concurrency":0}`,
		`{"concurrency":65}`,
		`{"token_refresh_min_lead":600,"token_refresh_max_lead":300}`,
		`{"rate_limit_base":120,"rate_limit_max":60}`,
		`{"max_attempts":11}`,
		`{"quota_refresh":60}`,
		`{"quota_refresh":7201}`,
		`{"low_quota":51}`,
		`{"strategy":"random"}`,
	} {
		if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/scheduling", nil, bad); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，实际 %d %s", bad, resp.StatusCode, readBody(resp))
		}
	}

	resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/scheduling", nil,
		`{"token_refresh_early":false,"concurrency":4,"server_error_cooldown":0,"quota_refresh":0,"low_quota":20,"strategy":"smart"}`)
	decode(t, resp, &got)
	if got.Settings.Concurrency != 4 || got.Settings.TokenRefreshEarly || got.Settings.ServerErrorCooldown != 0 || got.Settings.MaxAttempts != 3 {
		t.Fatalf("应只改给出的字段：%+v", got.Settings)
	}
	p := svc.relay.Policy()
	if p.Concurrency != 4 || p.TokenRefresh.Early || p.ServerErrorCooldown != 0 || p.QuotaRefresh != 0 || p.LowQuota != 0.2 || p.Strategy != pool.StrategySmart {
		t.Fatalf("修改应立即生效：%+v", p)
	}

	// 新服务（相当于重启）从库加载。
	other, err := NewService(context.Background(), svc.cfg, st, svc.enc)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(context.Background())
	if p := other.relay.Policy(); p.Concurrency != 4 || p.TokenRefresh.Early {
		t.Fatalf("重启后应从库加载：%+v", p)
	}
}

// 会话粘性的方式：默认严格；升级前存的设置只有 sticky 开关时，开着的换成严格、关着的换成关闭；
// 保存时 sticky 跟着方式写，撤销升级后的旧版本照样能读；方式取值校验。
func TestSchedulingStickyMode(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	ctx := context.Background()

	if p := svc.relay.Policy(); p.Sticky != relay.StickyStrict {
		t.Fatalf("默认应为严格：%q", p.Sticky)
	}
	for raw, want := range map[string]string{
		`{"sticky":false}`:                          relay.StickyOff,
		`{"sticky":true}`:                           relay.StickyStrict,
		`{"sticky":true,"sticky_mode":"preferred"}`: relay.StickyPreferred,
		`{"concurrency":4}`:                         relay.StickyStrict,
	} {
		if err := st.SetSetting(ctx, schedulingSetting.key, raw); err != nil {
			t.Fatal(err)
		}
		other, err := NewService(ctx, svc.cfg, st, svc.enc)
		if err != nil {
			t.Fatal(err)
		}
		if p := other.relay.Policy(); p.Sticky != want {
			t.Errorf("%s 加载后应为 %q，实际 %q", raw, want, p.Sticky)
		}
		other.Close(ctx)
	}

	var got struct {
		Settings schedulingView `json:"settings"`
	}
	decode(t, doReq(t, srv, http.MethodPut, "/api/admin/settings/scheduling", nil, `{"sticky_mode":"off"}`), &got)
	if got.Settings.StickyMode != relay.StickyOff || got.Settings.Sticky || svc.relay.Policy().Sticky != relay.StickyOff {
		t.Fatalf("关闭后 sticky 应为 false：%+v", got.Settings)
	}
	if raw, _, _ := st.GetSetting(ctx, schedulingSetting.key); !strings.Contains(raw, `"sticky":false`) || !strings.Contains(raw, `"sticky_mode":"off"`) {
		t.Fatalf("库里应同时写 sticky 和 sticky_mode：%s", raw)
	}
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/scheduling", nil, `{"sticky_mode":"always"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("不认识的方式应返回 400，实际 %d", resp.StatusCode)
	}
}

// 账号设置：单独设置并发上限，0 表示跟随全局；范围校验；账号不存在返回 404。
func TestAccountSettings(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	importAndKey(t, srv)

	var list struct {
		Accounts           []accountView `json:"accounts"`
		DefaultConcurrency int           `json:"default_concurrency"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/accounts", nil, ""), &list)
	if list.DefaultConcurrency != 15 || list.Accounts[0].MaxConcurrency != 0 {
		t.Fatalf("默认应跟随全局 15：%+v", list)
	}
	id := strconv.FormatInt(list.Accounts[0].ID, 10)

	var updated struct {
		Account accountView `json:"account"`
	}
	decode(t, doReq(t, srv, http.MethodPut, "/api/admin/accounts/"+id+"/settings", nil, `{"max_concurrency":3}`), &updated)
	if updated.Account.MaxConcurrency != 3 {
		t.Fatalf("应设为 3：%+v", updated.Account)
	}
	if limit := svc.relay.ConcurrencyLimit(list.Accounts[0].ID); limit != 3 {
		t.Fatalf("号池应已按新设置生效，实际 %d", limit)
	}
	for path, want := range map[string]int{
		"/api/admin/accounts/" + id + "/settings": http.StatusBadRequest,
		"/api/admin/accounts/999/settings":        http.StatusNotFound,
	} {
		body := `{"max_concurrency":65}`
		if want == http.StatusNotFound {
			body = `{"max_concurrency":2}`
		}
		if resp := doReq(t, srv, http.MethodPut, path, nil, body); resp.StatusCode != want {
			t.Fatalf("%s 应返回 %d，实际 %d", path, want, resp.StatusCode)
		}
	}
	decode(t, doReq(t, srv, http.MethodPut, "/api/admin/accounts/"+id+"/settings", nil, `{"max_concurrency":0}`), &updated)
	if updated.Account.MaxConcurrency != 0 || svc.relay.ConcurrencyLimit(list.Accounts[0].ID) != 15 {
		t.Fatalf("设回 0 应跟随全局：%+v", updated.Account)
	}
}

// 流式中途上游不再发数据：断开后客户端在已收到的内容后面收到一个错误事件。
func TestStreamIdleSendsErrorEvent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1internal:streamGenerateContent", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}]}}` + "\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.Handle("/", mockupstream.Handler())
	svc, _ := newTestServiceWith(t, mux)
	p := svc.relay.Policy()
	p.StreamIdleTimeout = 200 * time.Millisecond
	svc.relay.SetPolicy(p)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	auth := importAndKey(t, srv)

	resp := doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:streamGenerateContent?alt=sse", auth,
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	body := readBody(resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "partial") ||
		!strings.Contains(body, `"status":"DEADLINE_EXCEEDED"`) || !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("stream: %d %q", resp.StatusCode, body)
	}
}

// 流式中途上游推来错误帧：客户端在已收到的内容后面收到改写过的错误事件，不带上游原文。
func TestStreamErrorFrameSendsErrorEvent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1internal:streamGenerateContent", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}]}}` + "\n\n" +
			`data: {"error":{"code":500,"status":"INTERNAL","message":"backend shard 42 failed"}}` + "\n\n"))
	})
	mux.Handle("/", mockupstream.Handler())
	svc, _ := newTestServiceWith(t, mux)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	auth := importAndKey(t, srv)

	resp := doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:streamGenerateContent?alt=sse", auth,
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	body := readBody(resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "partial") || strings.Contains(body, "shard 42") ||
		!strings.Contains(body, `"code":503`) || !strings.Contains(body, `"status":"UNAVAILABLE"`) || !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("stream: %d %q", resp.StatusCode, body)
	}
}
