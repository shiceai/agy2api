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
