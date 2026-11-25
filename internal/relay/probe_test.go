package relay

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/store"
)

func TestProbe(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "a", "b", "c")
	for _, rt := range []string{"a", "b", "c"} {
		e.setCatalog(rt, "m1", "m2")
	}
	probe := func(rt, model string) ProbeResult {
		t.Helper()
		res, err := e.relay.Probe(logx.WithRequestID(ctx, "probe-"+rt+"-"+model), e.ids[rt], Request{Model: model, ClientIP: "10.0.0.9", UserAgent: "admin-ui"})
		if err != nil {
			t.Fatalf("probe %s: %v", rt, err)
		}
		return res
	}
	status := func(rt string) (string, string) {
		a, err := e.store.GetAccount(ctx, e.enc, e.ids[rt])
		if err != nil {
			t.Fatal(err)
		}
		return a.Status, a.DisabledReason
	}

	// 成功：回复正文，走正常的信封（模型、项目）。
	res := probe("a", "m1")
	if !res.OK || res.Status != http.StatusOK || res.Reply != "from a" || res.Model != "m1" {
		t.Fatalf("ok probe: %+v", res)
	}
	if env := e.up.lastBody["a"]; env["model"] != "m1" || env["project"] != "p-a" {
		t.Fatalf("envelope: %v", env)
	}

	// 模型不在目录里、账号不存在。
	if _, err := e.relay.Probe(ctx, e.ids["a"], Request{Model: "nope"}); !errors.Is(err, ErrProbeModel) {
		t.Fatalf("unknown model: %v", err)
	}
	if _, err := e.relay.Probe(ctx, 999, Request{Model: "m1"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}

	// 额度耗尽：和正常请求一样冷却。
	e.up.script["a"] = []int{http.StatusTooManyRequests}
	res = probe("a", "m2")
	if res.OK || res.Kind != string(antigravity.KindQuotaExhausted) || res.CooldownMs <= 0 {
		t.Fatalf("429 probe: %+v", res)
	}
	cooled := false
	for _, c := range e.relay.Cooldowns()[e.ids["a"]] {
		cooled = cooled || c.Model == "m2"
	}
	if !cooled {
		t.Fatal("want m2 cooling after probe 429")
	}

	// 条款封禁：停用账号。
	e.up.script["b"] = []int{tos403}
	res = probe("b", "m1")
	if res.OK || res.Disabled != ReasonTOSViolation {
		t.Fatalf("tos probe: %+v", res)
	}
	if s, reason := status("b"); s != store.StatusDisabled || reason != ReasonTOSViolation {
		t.Fatalf("b status: %s %q", s, reason)
	}
	// 停用的账号也能检测，通过后不自动启用。
	res = probe("b", "m1")
	if !res.OK {
		t.Fatalf("disabled account probe: %+v", res)
	}
	if s, _ := status("b"); s != store.StatusDisabled {
		t.Fatalf("b re-enabled by probe: %s", s)
	}

	// refresh token 失效：停用账号。
	e.up.revoked["c"] = true
	res = probe("c", "m1")
	if res.OK || res.Kind != string(antigravity.KindUnauthorized) || res.Disabled != ReasonInvalidGrant {
		t.Fatalf("revoked probe: %+v", res)
	}
	if s, _ := status("c"); s != store.StatusDisabled {
		t.Fatalf("c status: %s", s)
	}

	// 用量：拿到上游响应的检测和正常请求一样记一条（API Key 为 ProbeKeyID，调用方是管理端），不记并发占用；
	// 取令牌失败的（c）不记。
	rec, d := e.usageByRequest("probe-a-m1")
	if rec.APIKeyID != ProbeKeyID || rec.AccountID != e.ids["a"] || rec.StatusCode != http.StatusOK || rec.Stream ||
		rec.ClientIP != "10.0.0.9" || rec.UserAgent != "admin-ui" || rec.TotalTokens != 5 {
		t.Fatalf("probe success usage: %+v", rec)
	}
	if d.Slot != nil || len(d.Attempts) != 1 || d.Attempts[0].Result != "success" {
		t.Fatalf("probe success detail: %+v", d)
	}
	rec, d = e.usageByRequest("probe-a-m2")
	if rec.StatusCode != http.StatusTooManyRequests || d.Error == nil || d.Error.Kind != string(antigravity.KindQuotaExhausted) ||
		len(d.Attempts) != 1 || d.Attempts[0].CooldownMs <= 0 {
		t.Fatalf("probe 429 usage: %+v %+v", rec, d)
	}
	records, _ := e.store.RecentUsage(ctx, 20)
	for _, r := range records {
		if r.AccountID == e.ids["c"] {
			t.Fatalf("token failure should not be recorded: %+v", r)
		}
	}
}
