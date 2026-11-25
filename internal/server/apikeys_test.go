package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/store"
)

// 鉴权走内存缓存：管理端启停、删除 Key 后立即生效；使用后 last_used_at 会落库。
func TestAPIKeyCacheFollowsAdminChanges(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	resp := doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"cache-test"}`)
	var created struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
		Secret string `json:"secret"`
	}
	decode(t, resp, &created)
	auth := map[string]string{"x-goog-api-key": created.Secret}
	keyPath := fmt.Sprintf("/api/admin/keys/%d", created.Key.ID)

	// 管理接口字段统一为 snake_case。
	listBody := readBody(doReq(t, srv, http.MethodGet, "/api/admin/keys", nil, ""))
	for _, field := range []string{`"id"`, `"name"`, `"prefix"`, `"status"`, `"created_at"`, `"last_used_at"`} {
		if !strings.Contains(listBody, field) {
			t.Fatalf("Key 列表缺少字段 %s：%s", field, listBody)
		}
	}

	steps := []struct {
		name   string
		method string
		path   string // 空表示不做管理操作，只验证鉴权
		want   int
	}{
		{"新建后可用", "", "", http.StatusOK},
		{"停用后拒绝", http.MethodPost, keyPath + "/disable", http.StatusUnauthorized},
		{"启用后恢复", http.MethodPost, keyPath + "/enable", http.StatusOK},
		{"删除后拒绝", http.MethodDelete, keyPath, http.StatusUnauthorized},
	}
	for _, step := range steps {
		if step.path != "" {
			if resp := doReq(t, srv, step.method, step.path, nil, ""); resp.StatusCode != http.StatusOK {
				t.Fatalf("%s：管理操作返回 %d", step.name, resp.StatusCode)
			}
		}
		resp := doReq(t, srv, http.MethodGet, "/v1beta/models", auth, "")
		readBody(resp)
		if resp.StatusCode != step.want {
			t.Fatalf("%s：期望 %d，实际 %d", step.name, step.want, resp.StatusCode)
		}
	}

	if resp := doReq(t, srv, http.MethodGet, "/v1beta/models", map[string]string{"x-goog-api-key": "agy-wrong"}, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误的 Key 应被拒绝，实际 %d", resp.StatusCode)
	}

	// 删除前用过，last_used_at 应已异步写入（Key 已删，改查另一个新 Key）。
	resp = doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"touch-test"}`)
	decode(t, resp, &created)
	readBody(doReq(t, srv, http.MethodGet, "/v1beta/models", map[string]string{"x-goog-api-key": created.Secret}, ""))
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		keys, err := st.ListAPIKeys(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) == 1 && keys[0].LastUsedAt > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("使用后 last_used_at 应落库，实际 %+v", keys[0])
		}
	}
}

// Key 的限制：允许的模型（列表只返回允许的，其它模型 403）、到期（401）、Token 上限（每月超出 429 带
// Retry-After，总量超出 403），修改后立即生效；列表里带本周期已用和重置时间。
func TestAPIKeyLimits(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/import", nil, `{"refresh_token":"rt"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("import: %d %s", resp.StatusCode, readBody(resp))
	}
	resp := doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"limited","allowed_models":[" gemini-3-pro-* ","GEMINI-3-PRO-*",""]}`)
	var created struct {
		Key struct {
			ID            int64    `json:"id"`
			AllowedModels []string `json:"allowed_models"`
		} `json:"key"`
		Secret string `json:"secret"`
	}
	decode(t, resp, &created)
	if len(created.Key.AllowedModels) != 1 || created.Key.AllowedModels[0] != "gemini-3-pro-*" {
		t.Fatalf("allowed models not normalized: %v", created.Key.AllowedModels)
	}
	auth := map[string]string{"x-goog-api-key": created.Secret}
	keyPath := fmt.Sprintf("/api/admin/keys/%d", created.Key.ID)
	generate := func(model string) *http.Response {
		return doReq(t, srv, http.MethodPost, "/v1beta/models/"+model+":generateContent", auth, `{"contents":[{"role":"user","parts":[{"text":"hi there"}]}]}`)
	}
	// 用量记录异步落库；改限制会从库里重算用量，先等记录写进库，测试才确定。mock 每个请求 5 个 token。
	waitTokens := func(want int64) {
		t.Helper()
		for i := 0; i < 100; i++ {
			if n, _ := st.APIKeyTokensSince(context.Background(), created.Key.ID, 0); n >= want {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("usage of %d tokens not written", want)
	}
	update := func(body string) {
		t.Helper()
		if resp := doReq(t, srv, http.MethodPut, keyPath, nil, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("update %s: %d %s", body, resp.StatusCode, readBody(resp))
		}
	}

	// 允许的模型
	list := readBody(doReq(t, srv, http.MethodGet, "/v1beta/models", auth, ""))
	if !strings.Contains(list, "gemini-3-pro-high") || strings.Contains(list, "gemini-3-flash-high") {
		t.Fatalf("model list not filtered: %s", list)
	}
	if resp := generate("gemini-3-flash-high"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disallowed model: %d", resp.StatusCode)
	}
	if resp := doReq(t, srv, http.MethodGet, "/v1beta/models/gemini-3-flash-high", auth, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disallowed get model: %d", resp.StatusCode)
	}
	if resp := generate("gemini-3-pro-high"); resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed model: %d %s", resp.StatusCode, readBody(resp))
	}

	// 到期
	update(fmt.Sprintf(`{"name":"limited","expires_at":%d}`, time.Now().Add(-time.Second).UnixMilli()))
	if resp := doReq(t, srv, http.MethodGet, "/v1beta/models", auth, ""); resp.StatusCode != http.StatusUnauthorized ||
		!strings.Contains(readBody(resp), "expired") {
		t.Fatalf("expired key: %d", resp.StatusCode)
	}

	// 每月 Token 上限 6：本月已用 5，还能再发一个请求（用到 10），之后被拒，带 Retry-After。
	waitTokens(5)
	update(`{"name":"limited","token_limit":6,"token_period":"month"}`)
	if resp := generate("gemini-3-flash-high"); resp.StatusCode != http.StatusOK {
		t.Fatalf("first request under limit: %d %s", resp.StatusCode, readBody(resp))
	}
	resp = generate("gemini-3-flash-high")
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("monthly quota: %d retry-after %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	// 列表：本周期已用、重置时间、近期用量
	var keys struct {
		Keys []struct {
			ID            int64 `json:"id"`
			TokenLimit    int64 `json:"token_limit"`
			PeriodUsed    int64 `json:"period_used"`
			PeriodResetAt int64 `json:"period_reset_at"`
			Expired       bool  `json:"expired"`
			Recent        struct {
				Requests int64 `json:"requests"`
				Tokens   int64 `json:"tokens"`
			} `json:"recent"`
		} `json:"keys"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/keys", nil, ""), &keys)
	k := keys.Keys[0]
	if k.TokenLimit != 6 || k.PeriodUsed < 5 || k.PeriodResetAt <= time.Now().UnixMilli() || k.Expired || k.Recent.Requests < 2 {
		t.Fatalf("key view: %+v", k)
	}

	// 总量上限用完：403
	waitTokens(10)
	update(`{"name":"limited","token_limit":6,"token_period":"total"}`)
	if resp := generate("gemini-3-flash-high"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("total quota: %d", resp.StatusCode)
	}
	// 放开后恢复
	update(`{"name":"limited"}`)
	if resp := generate("gemini-3-flash-high"); resp.StatusCode != http.StatusOK {
		t.Fatalf("no limits: %d", resp.StatusCode)
	}

	// 校验
	for _, body := range []string{`{"token_limit":5,"token_period":"week"}`, `{"token_limit":-1}`, `{"allowed_models":["gemini 3"]}`} {
		if resp := doReq(t, srv, http.MethodPut, keyPath, nil, body); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", body, resp.StatusCode)
		}
	}
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/keys/999", nil, `{}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown key: %d", resp.StatusCode)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"gemini-3-pro-high", "gemini-3-pro-high", true},
		{"gemini-3-pro-high", "gemini-3-pro-low", false},
		{"gemini-*", "gemini-2.5-flash", true},
		{"gemini-*", "claude-sonnet-5-5", false},
		{"*", "anything", true},
		{"*-flash", "gemini-3.8-flash", true},
		{"gemini-*-flash", "gemini-3.8-flash", true},
		{"gemini-*-flash", "gemini-3.8-flash-high", false},
		{"claude-*-5-5*", "claude-opus-5-5-high", true},
		{"ab*b", "ab", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestTokenPeriod(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 12, 31, 23, 30, 0, 0, loc)
	if got := periodStart("day", now); !got.Equal(time.Date(2026, 12, 31, 0, 0, 0, 0, loc)) {
		t.Errorf("day start: %v", got)
	}
	if got := periodEnd("day", now); !got.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, loc)) {
		t.Errorf("day end: %v", got)
	}
	if got := periodStart("month", now); !got.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, loc)) {
		t.Errorf("month start: %v", got)
	}
	if got := periodEnd("month", now); !got.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, loc)) {
		t.Errorf("month end: %v", got)
	}
	if got := periodStart("total", now); got.UnixMilli() != 0 || !periodEnd("total", now).IsZero() {
		t.Errorf("total period: %v", got)
	}
}

func TestKeyStatsAndModelNames(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	auth := importAndKey(t, srv)
	if resp := doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent", auth, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("generate: %d", resp.StatusCode)
	}
	var names struct {
		Models   []string `json:"models"`
		Families []string `json:"families"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/models", nil, ""), &names)
	if !strings.Contains(strings.Join(names.Models, ","), "gemini-3-pro-high") || !strings.Contains(strings.Join(names.Families, ","), "gemini-3-pro") {
		t.Fatalf("model names: %+v", names)
	}

	var keys struct {
		Keys []struct {
			ID int64 `json:"id"`
		} `json:"keys"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/keys", nil, ""), &keys)
	path := fmt.Sprintf("/api/admin/keys/%d/stats?since=0", keys.Keys[0].ID)
	for i := 0; i < 100; i++ { // 用量异步落库
		var stats struct {
			Models []struct {
				Model    string `json:"model"`
				Requests int64  `json:"requests"`
				Success  int64  `json:"success"`
			} `json:"models"`
		}
		decode(t, doReq(t, srv, http.MethodGet, path, nil, ""), &stats)
		if len(stats.Models) == 1 {
			if m := stats.Models[0]; m.Model != "gemini-3-pro-high" || m.Requests != 1 || m.Success != 1 {
				t.Fatalf("stats: %+v", m)
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("key stats never showed the request")
}

func TestKeyAllowsModel(t *testing.T) {
	k := &store.APIKey{APIKeySettings: store.APIKeySettings{AllowedModels: []string{"gemini-3.8-flash", "Claude-*"}}}
	cases := map[string]bool{
		"gemini-3.8-flash":         true,  // 家族名本身
		"gemini-3.8-flash-high":    true,  // 家族的各档
		"gemini-3.8-flash-lite":    false, // 不是档位后缀，是另一个模型
		"claude-sonnet-5-5-medium": true,  // 通配，不分大小写
		"gemini-2.5-flash":         false,
	}
	for model, want := range cases {
		if got := keyAllowsModel(k, model); got != want {
			t.Errorf("keyAllowsModel(%q) = %v, want %v", model, got, want)
		}
	}
	if !keyAllowsModel(&store.APIKey{}, "anything") || !keyAllowsModel(nil, "anything") {
		t.Error("no restriction should allow everything")
	}
}

// 被 Key 的限制拒绝的生成请求也记一条用量（不允许的模型 403、额度用完 403/429），计数请求不记。
func TestKeyRejectionsRecorded(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/import", nil, `{"refresh_token":"rt"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("import: %d", resp.StatusCode)
	}
	var created struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
		Secret string `json:"secret"`
	}
	decode(t, doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"r","allowed_models":["gemini-3-pro-*"],"token_limit":1,"token_period":"total"}`), &created)
	auth := map[string]string{"x-goog-api-key": created.Secret}
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	post := func(path string) int {
		resp := doReq(t, srv, http.MethodPost, path, auth, body)
		readBody(resp)
		return resp.StatusCode
	}
	if code := post("/v1beta/models/gemini-3-flash-high:generateContent"); code != http.StatusForbidden {
		t.Fatalf("not allowed: %d", code)
	}
	if code := post("/v1beta/models/gemini-3-flash-high:countTokens"); code != http.StatusForbidden {
		t.Fatalf("count not allowed: %d", code)
	}
	if code := post("/v1beta/models/gemini-3-pro-high:generateContent"); code != http.StatusOK {
		t.Fatalf("first allowed: %d", code)
	}
	if code := post("/v1beta/models/gemini-3-pro-high:streamGenerateContent"); code != http.StatusForbidden {
		t.Fatalf("quota exhausted: %d", code)
	}

	// 用量异步落库：等三条（不允许的模型、成功、额度用完）写进库，计数请求不记。
	want := map[string]int{"model_not_allowed": 0, "key_quota": 0}
	for i := 0; i < 100; i++ {
		recs, _ := st.RecentUsage(context.Background(), 10)
		for k := range want {
			want[k] = 0
		}
		for _, r := range recs {
			for k := range want {
				if strings.Contains(string(r.Detail), `"kind":"`+k+`"`) {
					want[k]++
					if r.APIKeyID != created.Key.ID || r.StatusCode != http.StatusForbidden {
						t.Fatalf("%s record: %+v", k, r)
					}
				}
			}
		}
		if want["model_not_allowed"] == 1 && want["key_quota"] == 1 && len(recs) == 3 {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("rejection records: %v", want)
}

// 关停时等记录最近使用时间的后台写入完成，再关库：Touch 之后马上 Close，时间也已经落库。
func TestTouchFlushedOnClose(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	_, key, err := st.CreateAPIKey(ctx, store.APIKeySettings{Name: "k"})
	if err != nil {
		t.Fatal(err)
	}
	svc.keys.Touch(key.ID)
	if err := svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	keys, err := st.ListAPIKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].ID != key.ID {
		t.Fatalf("读回密钥失败：%v %v", keys, err)
	}
	if keys[0].LastUsedAt == 0 {
		t.Fatal("Close 返回前应已写入最近使用时间")
	}
}
