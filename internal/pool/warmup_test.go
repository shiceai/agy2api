package pool

import (
	"errors"
	"testing"
	"time"
)

// 新号预热：刚导入的账号并发被压到初始值，累计够成功数后放开到正常上限。
func TestWarmupConcurrencyRamp(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	f.reload()
	f.pool.SetDefaultConcurrency(5)
	f.pool.SetWarmup(WarmupConfig{Enabled: true, InitialConcurrency: 1, Successes: 4, Age: time.Hour})

	// 新号（号龄≈0、成功 0）：并发上限压到 1。
	l1, err := f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
	if err != nil {
		t.Fatalf("第一个请求应有名额：%v", err)
	}
	_, err = f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
	var ue *UnavailableError
	if !errors.As(err, &ue) || !ue.Busy {
		t.Fatalf("预热期并发上限为 1，第二个应 Busy，实际 %v", err)
	}
	l1.Release()

	// 攒够成功数后放开到正常上限 5。
	for range 4 {
		f.pool.NoteSuccess(a, 0)
	}
	var held []*Lease
	for range 5 {
		l, err := f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
		if err != nil {
			t.Fatalf("预热完成后应放开到 5 个名额：%v", err)
		}
		held = append(held, l)
	}
	if n := f.pool.InFlight(a); n != 5 {
		t.Fatalf("预热完成后应能占满 5 个名额，实际 %d", n)
	}
	for _, l := range held {
		l.Release()
	}
}

// 请求节奏：同一账号两次发起之间要隔最小间隔，间隔内第二次暂不可用。
func TestWarmupPacing(t *testing.T) {
	f := newFixture(t)
	f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	f.reload()
	f.pool.SetDefaultConcurrency(5)
	// 两个进度维度都关 → 进度满 → 间隔恒为 MinInterval，隔离节奏逻辑。
	f.pool.SetWarmup(WarmupConfig{Enabled: true, InitialConcurrency: 5, InitialInterval: 500 * time.Millisecond, MinInterval: 500 * time.Millisecond})

	l, err := f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
	if err != nil {
		t.Fatalf("第一个请求应有名额：%v", err)
	}
	l.Release() // 节奏管的是发起间隔，和名额是否释放无关
	_, err = f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
	var ue *UnavailableError
	if !errors.As(err, &ue) || !ue.Busy || ue.RetryAt.IsZero() {
		t.Fatalf("间隔内第二次应 Busy 并给出 RetryAt，实际 %v", err)
	}
	if d := time.Until(ue.RetryAt); d <= 0 || d > 500*time.Millisecond {
		t.Fatalf("RetryAt 应在约 500ms 内，实际 %v", d)
	}
}

// 节奏挡住时等到放行就选上：节奏放行不会有名额释放来唤醒，不能白等到时限（线上见过白等 3.5 秒，最坏等满时限）。
func TestAcquireWakesWhenPacingAllows(t *testing.T) {
	f := newFixture(t)
	f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	f.reload()
	f.pool.SetDefaultConcurrency(5)
	f.pool.SetWarmup(WarmupConfig{Enabled: true, InitialConcurrency: 5, InitialInterval: 300 * time.Millisecond, MinInterval: 300 * time.Millisecond})

	l, err := f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
	if err != nil {
		t.Fatalf("第一个请求应有名额：%v", err)
	}
	l.Release() // 释放发生在下一个请求开始等之前，之后再没有释放可以唤醒它
	start := time.Now()
	l2, err := f.pool.Acquire(t.Context(), "gemini-2.5-flash", "medium", nil, 0, false, 5*time.Second)
	if err != nil {
		t.Fatalf("节奏放行后应选上：%v", err)
	}
	l2.Release()
	if d := time.Since(start); d < 200*time.Millisecond || d > 2*time.Second {
		t.Fatalf("应等约一个间隔（300ms）就选上，实际 %v", d)
	}
}

// 严格粘性：绑定的账号被节奏挡住时先等它，不借别的账号；不严格时直接用别的账号。
func TestStrictWaitsForPacedPrefer(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-2.5-flash")
	f.reload()
	f.pool.SetDefaultConcurrency(5)
	f.pool.SetWarmup(WarmupConfig{Enabled: true, InitialConcurrency: 5, InitialInterval: 300 * time.Millisecond, MinInterval: 300 * time.Millisecond})

	l, err := f.pool.Select("gemini-2.5-flash", "medium", nil, a)
	if err != nil || l.Account.ID != a {
		t.Fatalf("第一个请求应选中绑定账号 a：%v", err)
	}
	l.Release()

	// 严格：a 在间隔内，b 随时可用，仍等 a 放行。
	start := time.Now()
	la, err := f.pool.Acquire(t.Context(), "gemini-2.5-flash", "medium", nil, a, true, 5*time.Second)
	if err != nil || la.Account.ID != a {
		t.Fatalf("严格粘性应等绑定账号 a，实际 %v %v", la, err)
	}
	la.Release()
	if d := time.Since(start); d < 200*time.Millisecond || d > 2*time.Second {
		t.Fatalf("应等约一个间隔（300ms）就选上 a，实际 %v", d)
	}

	// 不严格：a 刚用过、又在间隔内，直接用 b。
	lb, err := f.pool.Acquire(t.Context(), "gemini-2.5-flash", "medium", nil, a, false, 5*time.Second)
	if err != nil || lb.Account.ID != b {
		t.Fatalf("不严格时应临时用 b，实际 %v %v", lb, err)
	}
	lb.Release()
}

// 租约里的上限是生效中的上限：预热中是压低后的值（用量详情显示"在用/上限"）。
func TestLeaseLimitReflectsWarmup(t *testing.T) {
	f := newFixture(t)
	f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	f.reload()
	f.pool.SetDefaultConcurrency(5)
	f.pool.SetWarmup(WarmupConfig{Enabled: true, InitialConcurrency: 1, Successes: 4, Age: time.Hour})

	l, err := f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if l.Limit != 1 {
		t.Fatalf("新号预热初期租约上限应为 1，实际 %d", l.Limit)
	}
}

// 关掉预热后一切照旧：新号也能占满正常上限，没有间隔限制。
func TestWarmupDisabled(t *testing.T) {
	f := newFixture(t)
	f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	f.reload()
	f.pool.SetDefaultConcurrency(3)
	f.pool.SetWarmup(WarmupConfig{Enabled: false, InitialConcurrency: 1, Successes: 10, Age: time.Hour})

	var held []*Lease
	for range 3 {
		l, err := f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
		if err != nil {
			t.Fatalf("预热关闭时新号应能占满 3 个名额：%v", err)
		}
		held = append(held, l)
	}
	for _, l := range held {
		l.Release()
	}
}
