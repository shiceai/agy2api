package pool

import (
	"context"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/store"
)

// 严格粘性：优先账号并发已满时先等它的名额，不借别的账号；等不到再借。不等时（waitPrefer 为 false）直接借。
func TestAcquireWaitsForPreferred(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-3-flash")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-3-flash")
	f.reload()
	f.pool.SetDefaultConcurrency(1)
	held, err := f.pool.Select("gemini-3-flash", "medium", map[int64]bool{b: true}, 0) // 占住 a 的名额
	if err != nil || held.Account.ID != a {
		t.Fatalf("应占住 a：%v %v", held, err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		held.Release()
	}()
	start := time.Now()
	l, err := f.pool.Acquire(context.Background(), "gemini-3-flash", "medium", nil, a, true, 2*time.Second)
	if err != nil || l.Account.ID != a || l.Affinity != AffinityHit || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("应等到 a 的名额：%v %v（等了 %v）", l, err, time.Since(start))
	}

	// a 一直满：等完 wait 后借 b，原因记为并发已满。
	start = time.Now()
	l2, err := f.pool.Acquire(context.Background(), "gemini-3-flash", "medium", nil, a, true, 100*time.Millisecond)
	if err != nil || l2.Account.ID != b || l2.Affinity != AffinityBusy || time.Since(start) < 90*time.Millisecond {
		t.Fatalf("等不到 a 时应借 b：%v %v（等了 %v）", l2, err, time.Since(start))
	}
	l2.Release()

	// 不等：立即借 b。
	start = time.Now()
	l3, err := f.pool.Acquire(context.Background(), "gemini-3-flash", "medium", nil, a, false, time.Second)
	if err != nil || l3.Account.ID != b || time.Since(start) > 50*time.Millisecond {
		t.Fatalf("优先模式应立即借 b：%v %v（等了 %v）", l3, err, time.Since(start))
	}
	l3.Release()

	// a 额度低于避让阈值：空出来也会先避开它，不等。
	f.setQuota(a, map[string]*float64{"gemini-3-flash": frac(0.05)})
	f.pool.SetLowQuota(0.1)
	start = time.Now()
	l4, err := f.pool.Acquire(context.Background(), "gemini-3-flash", "medium", nil, a, true, time.Second)
	if err != nil || l4.Account.ID != b || time.Since(start) > 50*time.Millisecond {
		t.Fatalf("额度低的 a 不应等：%v %v（等了 %v）", l4, err, time.Since(start))
	}
	l4.Release()
	l.Release()
}

// Recoverable：冷却、代理暂停在窗口内结束的算能恢复；超出窗口、每周额度用完、额度低于避让阈值、不在号池或不支持模型的不算。
func TestRecoverable(t *testing.T) {
	f := newFixture(t)
	ids := map[string]int64{}
	for _, name := range []string{"ok", "short", "long", "paused", "pausedLong", "low", "weekly"} {
		ids[name] = f.addAccount(name+"@example.com", "p-"+name, "enabled", "gemini-3-flash")
	}
	f.reload()
	f.cooldown(ids["short"], "gemini-3-flash", time.Minute)
	f.cooldown(ids["long"], "gemini-3-flash", time.Hour)
	f.pool.Pause(ids["paused"], time.Now().Add(time.Minute))
	f.pool.Pause(ids["pausedLong"], time.Now().Add(time.Hour))
	f.setQuota(ids["low"], map[string]*float64{"gemini-3-flash": frac(0.05)})
	f.setWeekly(ids["weekly"], store.WeeklyQuotaOn, frac(0), time.Now().Add(48*time.Hour))
	f.pool.SetLowQuota(0.1)

	want := map[string]bool{"ok": true, "short": true, "long": false, "paused": true, "pausedLong": false, "low": false, "weekly": false}
	for name, w := range want {
		if got := f.pool.Recoverable(ids[name], "gemini-3-flash", "medium", HoldWindow); got != w {
			t.Errorf("%s: Recoverable = %v，应为 %v", name, got, w)
		}
	}
	if f.pool.Recoverable(ids["ok"], "gemini-9-pro", "medium", HoldWindow) || f.pool.Recoverable(999, "gemini-3-flash", "medium", HoldWindow) {
		t.Fatal("不支持的模型、不在号池的账号不算能恢复")
	}
}
