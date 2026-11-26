package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shiceai/agy2api/internal/antigravity"
)

type weeklyAccount struct {
	ID             int64                     `json:"id"`
	MaxConcurrency int                       `json:"max_concurrency"`
	WeeklyQuota    string                    `json:"weekly_quota"`
	WeeklyQuotaOn  bool                      `json:"weekly_quota_on"`
	QuotaSummary   *antigravity.QuotaSummary `json:"quota_summary"`
	QuotaSummaryAt int64                     `json:"quota_summary_at"`
	Subscription   *struct {
		Credits *float64 `json:"credits"`
	} `json:"subscription"`
}

// 每周额度：默认不拉；账号单独开启时立即拉一次，账号视图带分组额度；账号跟随全局时看调度设置里的开关；
// 开关的取值不对返回 400，只改并发时不动它。订阅里解析出 AI 积分。
func TestWeeklyQuota(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	importAndKey(t, srv)

	listAccounts := func() (weeklyAccount, bool) {
		t.Helper()
		var list struct {
			Accounts    []weeklyAccount `json:"accounts"`
			WeeklyQuota bool            `json:"weekly_quota"`
		}
		decode(t, doReq(t, srv, http.MethodGet, "/api/admin/accounts", nil, ""), &list)
		if len(list.Accounts) != 1 {
			t.Fatalf("accounts = %+v", list.Accounts)
		}
		return list.Accounts[0], list.WeeklyQuota
	}
	put := func(id int64, body string, status int) weeklyAccount {
		t.Helper()
		resp := doReq(t, srv, http.MethodPut, fmt.Sprintf("/api/admin/accounts/%d/settings", id), nil, body)
		if resp.StatusCode != status {
			t.Fatalf("PUT %s: %d %s", body, resp.StatusCode, readBody(resp))
		}
		var out struct {
			Account weeklyAccount `json:"account"`
		}
		if status == http.StatusOK {
			decode(t, resp, &out)
		} else {
			_ = readBody(resp)
		}
		return out.Account
	}

	a, global := listAccounts()
	if global || a.WeeklyQuota != "" || a.WeeklyQuotaOn || a.QuotaSummary != nil {
		t.Fatalf("默认不应拉每周额度：global=%v %+v", global, a)
	}
	if a.Subscription == nil || a.Subscription.Credits == nil || *a.Subscription.Credits != 25 {
		t.Fatalf("应解析出 AI 积分 25：%+v", a.Subscription)
	}

	put(a.ID, `{"weekly_quota":"maybe"}`, http.StatusBadRequest)
	put(a.ID, `{}`, http.StatusBadRequest)

	on := put(a.ID, `{"weekly_quota":"on"}`, http.StatusOK)
	if on.WeeklyQuota != "on" || !on.WeeklyQuotaOn || on.QuotaSummary == nil || on.QuotaSummaryAt == 0 {
		t.Fatalf("开启后应立即拉到每周额度：%+v", on)
	}
	raw, _ := json.Marshal(on.QuotaSummary)
	for _, want := range []string{`"display_name":"Gemini Models"`, `"window":"weekly","remaining_fraction":0.75`, `"bucket_id":"3p-5h","window":"5h","reset_time"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("分组额度里应有 %s：%s", want, raw)
		}
	}

	// 只改并发不动每周额度开关。
	if got := put(a.ID, `{"max_concurrency":3}`, http.StatusOK); got.MaxConcurrency != 3 || got.WeeklyQuota != "on" {
		t.Fatalf("只改并发后 = %+v", got)
	}

	// 跟随全局：全局关时不拉，打开调度设置里的开关后跟随全局的账号也算开。
	if got := put(a.ID, `{"weekly_quota":""}`, http.StatusOK); got.WeeklyQuota != "" || got.WeeklyQuotaOn {
		t.Fatalf("跟随全局（关）= %+v", got)
	}
	var sched struct {
		Settings map[string]any `json:"settings"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/settings/scheduling", nil, ""), &sched)
	sched.Settings["weekly_quota"] = true
	body, _ := json.Marshal(sched.Settings)
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/scheduling", nil, string(body)); resp.StatusCode != http.StatusOK {
		t.Fatalf("save scheduling: %d %s", resp.StatusCode, readBody(resp))
	}
	if a, global := listAccounts(); !global || !a.WeeklyQuotaOn {
		t.Fatalf("全局开启后跟随全局的账号应算开：global=%v %+v", global, a)
	}
	if got := put(a.ID, `{"weekly_quota":"off"}`, http.StatusOK); got.WeeklyQuotaOn {
		t.Fatalf("账号单独关闭应优先于全局：%+v", got)
	}
}
