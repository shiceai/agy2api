package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/store"
)

// setWeekly 给账号设每周额度开关（空串跟随全局、on、off）和 Gemini 组的每周额度（remaining 为 nil 表示上游省略了比例），
// 然后重新加载。
func (f *fixture) setWeekly(id int64, mode string, remaining *float64, reset time.Time) {
	f.t.Helper()
	ctx := context.Background()
	summary := antigravity.QuotaSummary{Groups: []antigravity.QuotaGroup{{
		DisplayName: "Gemini Models",
		Buckets: []antigravity.QuotaBucket{
			{Window: "5h", RemainingFraction: frac(1), ResetTime: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
			{Window: "weekly", RemainingFraction: remaining, ResetTime: reset.UTC().Format(time.RFC3339)},
		},
	}}}
	if err := f.store.SetAccountQuotaSummary(ctx, id, antigravity.MarshalQuotaSummary(summary)); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.SetAccountWeeklyQuota(ctx, id, mode); err != nil {
		f.t.Fatal(err)
	}
	f.reload()
}

// 每周额度用完的账号在重置前不选（都用完时返回最早的重置时间）；账号关了每周额度、全局开关关着而账号跟随全局、
// 或已过重置时间时，旧数据不起作用。
func TestWeeklyExhaustedSkipped(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-4-neon-high")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-4-neon-high")
	soon, later := time.Now().Add(30*time.Hour).Truncate(time.Second), time.Now().Add(60*time.Hour).Truncate(time.Second)
	f.setWeekly(a, store.WeeklyQuotaOn, frac(0), soon)

	if got := f.selectIDs("gemini-4-neon-high", 4); got[a] != 0 || got[b] != 4 {
		t.Fatalf("每周额度用完的 a 不应被选：%v", got)
	}

	// 两个都用完（b 的比例被上游省略）：选不到，RetryAt 是较早的重置时间。
	f.setWeekly(b, store.WeeklyQuotaOn, nil, later)
	_, _, err := f.pick("gemini-4-neon-high", "medium")
	var ue *UnavailableError
	if !errors.As(err, &ue) || !ue.RetryAt.Equal(soon) {
		t.Fatalf("都用完时应返回最早的重置时间 %v：%v", soon, err)
	}

	// a 关掉每周额度：旧数据不起作用，a 能选。
	f.setWeekly(a, store.WeeklyQuotaOff, frac(0), soon)
	if got := f.selectIDs("gemini-4-neon-high", 2); got[a] != 2 {
		t.Fatalf("关了每周额度的 a 应能选：%v", got)
	}
	// a 跟随全局：全局关时能选，全局开时又不选。
	f.setWeekly(a, "", frac(0), soon)
	if got := f.selectIDs("gemini-4-neon-high", 2); got[a] != 2 {
		t.Fatalf("跟随全局（关）的 a 应能选：%v", got)
	}
	f.pool.SetWeeklyQuota(true)
	if _, _, err := f.pick("gemini-4-neon-high", "medium"); !errors.As(err, &ue) {
		t.Fatalf("全局开启后 a、b 都用完，应选不到：%v", err)
	}
	// 已过重置时间、还没拉到新数据：按已恢复算。
	f.setWeekly(a, store.WeeklyQuotaOn, frac(0), time.Now().Add(-time.Minute))
	if got := f.selectIDs("gemini-4-neon-high", 2); got[a] != 2 {
		t.Fatalf("过了重置时间的 a 应能选：%v", got)
	}
}

// 避让按 5 小时和每周额度的较低值：5 小时充足而每周额度低于阈值的账号先不选。
func TestWeeklyLowQuotaAvoidance(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-3-flash")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-3-flash")
	f.setQuota(a, map[string]*float64{"gemini-3-flash": frac(0.9)})
	f.setQuota(b, map[string]*float64{"gemini-3-flash": frac(0.5)})
	f.setWeekly(a, store.WeeklyQuotaOn, frac(0.05), time.Now().Add(48*time.Hour))
	f.pool.SetLowQuota(0.1)

	if got := f.selectIDs("gemini-3-flash", 4); got[a] != 0 || got[b] != 4 {
		t.Fatalf("每周额度低于阈值的 a 应先避开：%v", got)
	}
	f.setWeekly(a, store.WeeklyQuotaOn, frac(0.6), time.Now().Add(48*time.Hour))
	if got := f.selectIDs("gemini-3-flash", 4); got[a] != 2 || got[b] != 2 {
		t.Fatalf("每周额度充足时两个都选：%v", got)
	}
}

// 5 小时额度恢复了、每周额度还是 0：不提前解除额度耗尽冷却；每周额度也恢复后才解除。
func TestReconcileKeepsWeeklyExhausted(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-3-flash")
	f.setWeekly(a, store.WeeklyQuotaOn, frac(0), time.Now().Add(48*time.Hour))
	f.pool.Cooldown(ctx, a, time.Now().Add(3*time.Hour), string(antigravity.KindQuotaExhausted), "gemini-3-flash")
	time.Sleep(5 * time.Millisecond)
	f.setQuota(a, map[string]*float64{"gemini-3-flash": frac(1)})

	if n := f.pool.ReconcileCooldowns(ctx); n != 0 {
		t.Fatalf("每周额度还是 0 时不应解除冷却：解除了 %d 条", n)
	}
	f.setWeekly(a, store.WeeklyQuotaOn, frac(0.4), time.Now().Add(48*time.Hour))
	if n := f.pool.ReconcileCooldowns(ctx); n != 1 {
		t.Fatalf("每周额度恢复后应解除冷却：解除了 %d 条", n)
	}
}
