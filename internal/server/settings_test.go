package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 管理端修改默认档位后，家族名请求立即按新档位解析；非法值返回 400。
func TestDefaultTierSetting(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	auth := importAndKey(t, srv)

	resolved := func() string {
		t.Helper()
		var record struct {
			DisplayName string `json:"displayName"`
		}
		decode(t, doReq(t, srv, http.MethodGet, "/v1beta/models/gemini-3-pro", auth, ""), &record)
		return record.DisplayName
	}
	getTier := func() string {
		t.Helper()
		var res struct {
			Settings settingsView `json:"settings"`
			Tiers    []string     `json:"tiers"`
		}
		decode(t, doReq(t, srv, http.MethodGet, "/api/admin/settings", nil, ""), &res)
		if len(res.Tiers) != 4 {
			t.Fatalf("应返回可选档位，实际 %v", res.Tiers)
		}
		return res.Settings.DefaultTier
	}

	if tier := getTier(); tier != "medium" {
		t.Fatalf("未配置时默认档位应为 medium，实际 %q", tier)
	}
	if got := resolved(); got != "Gemini 3 Pro (Low)" {
		t.Fatalf("medium 缺失时应先往低取 low，实际 %q", got)
	}

	if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings", nil, `{"default_tier":"HIGH"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("修改设置应返回 200，实际 %d %s", resp.StatusCode, readBody(resp))
	}
	if tier := getTier(); tier != "high" {
		t.Fatalf("默认档位应改为 high，实际 %q", tier)
	}
	if got := resolved(); got != "Gemini 3 Pro (High)" {
		t.Fatalf("改为 high 后应解析到 high，实际 %q", got)
	}

	for _, body := range []string{`{"default_tier":"turbo"}`, `{"default_tier":"minimal"}`, `{}`} {
		if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings", nil, body); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，实际 %d", body, resp.StatusCode)
		}
	}
	if tier := getTier(); tier != "high" {
		t.Fatalf("非法修改不应生效，实际 %q", tier)
	}
}

// 重启时从库加载默认档位；库里的值不合法时忽略。
func TestLoadSettings(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	for _, tc := range []struct{ stored, want string }{{"low", "low"}, {"bogus", "low"}, {"xhigh", "xhigh"}} {
		if err := st.SetSetting(ctx, settingDefaultTier, tc.stored); err != nil {
			t.Fatal(err)
		}
		if err := svc.loadSettings(ctx); err != nil {
			t.Fatal(err)
		}
		if got := svc.relay.DefaultTier(); got != tc.want {
			t.Fatalf("库里是 %q 时应为 %q，实际 %q", tc.stored, tc.want, got)
		}
	}
}

// 按家族设置档位：GET 返回可选家族，PUT 整体替换并立即生效，非法档位返回 400。
func TestModelTierSetting(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	auth := importAndKey(t, srv)

	var res struct {
		Settings settingsView `json:"settings"`
		Families []familyView `json:"families"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/settings", nil, ""), &res)
	// mock 目录：gemini-3-pro-low、gemini-3-pro-high、gemini-3-flash-high。
	if len(res.Families) != 2 || res.Families[0].Family != "gemini-3-flash" || res.Families[1].Family != "gemini-3-pro" ||
		len(res.Families[1].Tiers) != 2 || res.Families[1].Tiers[0] != "low" {
		t.Fatalf("可选家族不对：%+v", res.Families)
	}
	if len(res.Settings.ModelTiers) != 0 {
		t.Fatalf("未配置时按模型档位应为空，实际 %v", res.Settings.ModelTiers)
	}

	resolved := func() string {
		t.Helper()
		var record struct {
			DisplayName string `json:"displayName"`
		}
		decode(t, doReq(t, srv, http.MethodGet, "/v1beta/models/gemini-3-pro", auth, ""), &record)
		return record.DisplayName
	}
	put := func(body string) int {
		t.Helper()
		resp := doReq(t, srv, http.MethodPut, "/api/admin/settings", nil, body)
		readBody(resp)
		return resp.StatusCode
	}

	if code := put(`{"default_tier":"low","model_tiers":{"gemini-3-pro":"HIGH"}}`); code != http.StatusOK {
		t.Fatalf("修改应返回 200，实际 %d", code)
	}
	if got := resolved(); got != "Gemini 3 Pro (High)" {
		t.Fatalf("家族单独设置应优先于默认档位，实际 %q", got)
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/settings", nil, ""), &res)
	if res.Settings.ModelTiers["gemini-3-pro"] != "high" {
		t.Fatalf("应返回归一后的按模型档位，实际 %v", res.Settings.ModelTiers)
	}

	for _, body := range []string{
		`{"default_tier":"low","model_tiers":{"gemini-3-pro":"turbo"}}`,
		`{"default_tier":"low","model_tiers":{" ":"high"}}`,
	} {
		if code := put(body); code != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，实际 %d", body, code)
		}
	}
	if got := resolved(); got != "Gemini 3 Pro (High)" {
		t.Fatalf("非法修改不应生效，实际 %q", got)
	}

	// 不带 model_tiers 表示清空，回到默认档位。
	if code := put(`{"default_tier":"low"}`); code != http.StatusOK {
		t.Fatalf("修改应返回 200，实际 %d", code)
	}
	if got := resolved(); got != "Gemini 3 Pro (Low)" {
		t.Fatalf("清空后应回到默认档位，实际 %q", got)
	}
}

// 启动加载按模型档位：不合法的项忽略，合法的生效。
func TestLoadModelTiers(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, settingModelTiers, `{"gemini-3-pro":"high","gemini-3-flash":"bogus"}`); err != nil {
		t.Fatal(err)
	}
	if err := svc.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	got := svc.relay.ModelTiers()
	if len(got) != 1 || got["gemini-3-pro"] != "high" {
		t.Fatalf("应只加载合法项，实际 %v", got)
	}
	if err := st.SetSetting(ctx, settingModelTiers, `not json`); err != nil {
		t.Fatal(err)
	}
	if err := svc.loadSettings(ctx); err != nil {
		t.Fatalf("非法 JSON 应忽略而不是报错：%v", err)
	}
}
