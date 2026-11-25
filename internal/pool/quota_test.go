package pool

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
)

// setQuota 把账号目录里各模型的剩余额度改成给定值（nil 表示没有额度信息），并更新刷新时间，然后重新加载。
func (f *fixture) setQuota(id int64, quota map[string]*float64) {
	f.t.Helper()
	catalog := make([]antigravity.ModelInfo, 0, len(quota))
	for m, rem := range quota {
		catalog = append(catalog, antigravity.ModelInfo{ID: m, RemainingFraction: rem})
	}
	if err := f.store.UpdateAccountModels(context.Background(), id, antigravity.MarshalModels(catalog)); err != nil {
		f.t.Fatalf("update models: %v", err)
	}
	f.reload()
}

func frac(v float64) *float64 { return &v }

func TestLowQuotaAvoidance(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-3-flash")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-3-flash")
	c := f.addAccount("c@example.com", "p-c", "enabled", "gemini-3-flash")
	f.setQuota(a, map[string]*float64{"gemini-3-flash": frac(0.05)})
	f.setQuota(b, map[string]*float64{"gemini-3-flash": frac(0.6)})
	f.setQuota(c, map[string]*float64{"gemini-3-flash": nil}) // 没有额度信息，视为正常

	// 未设置阈值时不避让。
	if got := f.selectIDs("gemini-3-flash", 6); got[a] != 2 || got[b] != 2 || got[c] != 2 {
		t.Fatalf("no threshold: %v", got)
	}

	f.pool.SetLowQuota(0.1)
	if got := f.selectIDs("gemini-3-flash", 6); got[a] != 0 || got[b] != 3 || got[c] != 3 {
		t.Fatalf("threshold 10%%: want a avoided, got %v", got)
	}

	// 其他账号都不可用时，额度低的照样选。
	f.cooldown(b, "gemini-3-flash", time.Minute)
	f.cooldown(c, "gemini-3-flash", time.Minute)
	if got := f.selectIDs("gemini-3-flash", 2); got[a] != 2 {
		t.Fatalf("only low account left: %v", got)
	}
}

func TestSharedQuota(t *testing.T) {
	f := newFixture(t)
	id := f.addAccount("a@example.com", "p-a", "enabled", "m")
	day, week := "2026-11-23T10:00:00Z", "2026-11-29T10:00:00Z"
	catalog := []antigravity.ModelInfo{
		{ID: "g1", RemainingFraction: frac(0.5), ResetTime: day},
		{ID: "g2", RemainingFraction: frac(0.5), ResetTime: day},
		{ID: "argon", RemainingFraction: frac(0.5), ResetTime: week},
		// 没用过的窗口都是 100%，重置时间相同也不能说明同窗口
		{ID: "c1", RemainingFraction: frac(1), ResetTime: day},
		{ID: "c2", RemainingFraction: frac(1), ResetTime: day},
		{ID: "free", RemainingFraction: frac(0.5)},
	}
	if err := f.store.UpdateAccountModels(context.Background(), id, antigravity.MarshalModels(catalog)); err != nil {
		t.Fatalf("update models: %v", err)
	}
	f.reload()
	a := f.pool.Accounts()[0]
	cases := map[string]string{"g1": "[g2]", "g2": "[g1]", "argon": "[]", "c1": "[]", "free": "[]", "missing": "[]"}
	for model, want := range cases {
		if got := fmt.Sprint(a.SharedQuota(model)); got != want {
			t.Errorf("SharedQuota(%s) = %s, want %s", model, got, want)
		}
	}
}

func TestLowQuotaAllLow(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "m")
	b := f.addAccount("b@example.com", "p-b", "enabled", "m")
	f.setQuota(a, map[string]*float64{"m": frac(0.02)})
	f.setQuota(b, map[string]*float64{"m": frac(0)})
	f.pool.SetLowQuota(0.1)
	if got := f.selectIDs("m", 4); got[a] != 2 || got[b] != 2 {
		t.Fatalf("all low: want no avoidance, got %v", got)
	}
}

func TestReconcileCooldowns(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "m1", "m2", "m3", "m4")
	recovered := map[string]*float64{"m1": frac(1), "m2": frac(0), "m3": frac(1), "m4": frac(1)}
	f.setQuota(a, recovered)
	time.Sleep(5 * time.Millisecond)

	quota := string(antigravity.KindQuotaExhausted)
	f.pool.Cooldown(ctx, a, time.Now().Add(3*time.Hour), quota, "m1")        // 额度已恢复：解除
	f.pool.Cooldown(ctx, a, time.Now().Add(3*time.Hour), quota, "m2")        // 额度仍是 0：保留
	f.pool.Cooldown(ctx, a, time.Now().Add(72*time.Hour), quota, "m3")       // 周额度（超过 24 小时）：保留
	f.pool.Cooldown(ctx, a, time.Now().Add(3*time.Hour), "rate_limit", "m4") // 不是额度耗尽：保留

	// 额度是冷却之前取的，不能据此解除。
	if n := f.pool.ReconcileCooldowns(ctx); n != 0 {
		t.Fatalf("stale quota cleared %d cooldowns", n)
	}

	time.Sleep(5 * time.Millisecond)
	f.setQuota(a, recovered)
	if n := f.pool.ReconcileCooldowns(ctx); n != 1 {
		t.Fatalf("cleared %d cooldowns, want 1", n)
	}
	left := map[string]bool{}
	for _, c := range f.pool.Cooldowns()[a] {
		left[c.Model] = true
	}
	if left["m1"] || !left["m2"] || !left["m3"] || !left["m4"] {
		t.Fatalf("cooldowns left: %v", left)
	}

	// 库里也删了：重启后读回不再有 m1。
	rows, err := f.store.ActiveCooldowns(ctx, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rows {
		if c.Model == "m1" {
			t.Fatal("m1 cooldown still in store")
		}
	}
	if len(rows) != 3 {
		t.Fatalf("store has %d cooldowns, want 3", len(rows))
	}
}
