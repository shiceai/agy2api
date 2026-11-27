package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 读设置：库里没有时用默认值，缺的字段取默认值；不是合法 JSON 或校验不过时整体用默认值。
func TestJSONSettingLoad(t *testing.T) {
	type demo struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	c := jsonSetting[demo]{
		key:      "demo",
		name:     "测试设置",
		defaults: func() demo { return demo{A: 1, B: "x"} },
		validate: func(v demo) error {
			if v.A < 0 {
				return errors.New("a must be >= 0")
			}
			return nil
		},
	}
	_, st := newTestService(t)
	ctx := context.Background()
	cases := []struct {
		name   string
		stored string // 空表示库里没有
		want   demo
	}{
		{"库里没有", "", demo{1, "x"}},
		{"缺的字段取默认值", `{"a":5}`, demo{5, "x"}},
		{"完整", `{"a":2,"b":"y"}`, demo{2, "y"}},
		{"不是合法 JSON", `{"a":`, demo{1, "x"}},
		{"校验不过", `{"a":-1,"b":"y"}`, demo{1, "x"}},
	}
	for _, tc := range cases {
		if tc.stored != "" {
			if err := st.SetSetting(ctx, c.key, tc.stored); err != nil {
				t.Fatal(err)
			}
		}
		got, err := c.load(ctx, st)
		if err != nil || got != tc.want {
			t.Errorf("%s：期望 %+v，实际 %+v %v", tc.name, tc.want, got, err)
		}
	}
}

// 客户端名称过滤、系统提示词、请求处理：默认值、非法模式 400、保存后读回、库里的值不合法时读到默认值。
func TestJSONSettingEndpoints(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	ctx := context.Background()

	type resp[T any] struct {
		Settings T `json:"settings"`
	}
	get := func(path string, out any) {
		t.Helper()
		decode(t, doReq(t, srv, http.MethodGet, path, nil, ""), out)
	}

	const cf = "/api/admin/settings/client-filter"
	var f resp[clientFilterView]
	get(cf, &f)
	if f.Settings != clientFilterSetting.defaults() {
		t.Fatalf("客户端过滤默认值不对：%+v", f.Settings)
	}
	if r := doReq(t, srv, http.MethodPut, cf, nil, `{"enabled":true,"mode":"bogus"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法模式应返回 400，实际 %d", r.StatusCode)
	}
	// 缺的字段取默认值（use_default_keywords 默认开）。
	decode(t, doReq(t, srv, http.MethodPut, cf, nil, `{"enabled":true,"mode":"block","rules":"foo"}`), &f)
	if want := (clientFilterView{Enabled: true, Mode: "block", UseDefaultKeywords: true, Rules: "foo"}); f.Settings != want {
		t.Fatalf("保存后应读回 %+v，实际 %+v", want, f.Settings)
	}

	const sp = "/api/admin/settings/system-prompt"
	var p resp[systemPromptView]
	if r := doReq(t, srv, http.MethodPut, sp, nil, `{"enabled":true,"mode":"bogus"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法模式应返回 400，实际 %d", r.StatusCode)
	}
	decode(t, doReq(t, srv, http.MethodPut, sp, nil, `{"enabled":true,"mode":"prepend","template":"T"}`), &p)
	if p.Settings != (systemPromptView{Enabled: true, Mode: "prepend", Template: "T"}) {
		t.Fatalf("系统提示词保存后读回不对：%+v", p.Settings)
	}
	// 模板可以为空，三种模式都用内置官方基线。
	decode(t, doReq(t, srv, http.MethodPut, sp, nil, `{"enabled":true,"mode":"prepend","template":""}`), &p)
	if p.Settings != (systemPromptView{Enabled: true, Mode: "prepend", Template: ""}) {
		t.Fatalf("prepend 空模板应保存成功（用内置基线）：%+v", p.Settings)
	}
	decode(t, doReq(t, srv, http.MethodPut, sp, nil, `{"enabled":true,"mode":"as_user","template":""}`), &p)
	if p.Settings != (systemPromptView{Enabled: true, Mode: "as_user", Template: ""}) {
		t.Fatalf("as_user 空模板应保存成功：%+v", p.Settings)
	}

	const rh = "/api/admin/settings/request-handling"
	var h resp[requestHandlingView]
	get(rh, &h)
	if h.Settings != (requestHandlingView{RemoteImagesEnabled: false, ReturnThoughts: true}) {
		t.Fatalf("请求处理默认值不对：%+v", h.Settings)
	}
	decode(t, doReq(t, srv, http.MethodPut, rh, nil, `{"remote_images_enabled":true}`), &h)
	if h.Settings != (requestHandlingView{RemoteImagesEnabled: true, ReturnThoughts: true}) {
		t.Fatalf("请求处理保存后读回不对：%+v", h.Settings)
	}

	// 库里的模式不合法：读到的是默认值，和启动时实际生效的一致（以前会原样返回不合法的值）。
	if err := st.SetSetting(ctx, clientFilterSetting.key, `{"enabled":true,"mode":"bogus"}`); err != nil {
		t.Fatal(err)
	}
	get(cf, &f)
	if f.Settings != clientFilterSetting.defaults() {
		t.Fatalf("库里的值不合法时应读到默认值，实际 %+v", f.Settings)
	}
	if err := svc.loadClientFilter(ctx); err != nil {
		t.Fatal(err)
	}
}
