package pool

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSelectFiltersAndRoundRobin(t *testing.T) {
	f := newFixture(t)
	flash := f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash", "claude-sonnet")
	unknown := f.addAccount("b@example.com", "p-b", "enabled")                      // 目录未知，放行
	f.addAccount("c@example.com", "", "enabled", "gemini-2.5-flash")                // 无项目
	f.addAccount("d@example.com", "p-d", "disabled", "gemini-2.5-flash")            // 停用
	proOnly := f.addAccount("e@example.com", "p-e", "enabled", "gemini-3-pro-high") // 不含该模型
	f.reload()

	got := f.selectIDs("gemini-2.5-flash", 4)
	if len(got) != 2 || got[flash] != 2 || got[unknown] != 2 {
		t.Fatalf("应在 %d、%d 之间轮询各两次，实际 %v", flash, unknown, got)
	}
	got = f.selectIDs("gemini-3-pro-high", 4)
	if len(got) != 2 || got[proOnly] == 0 || got[unknown] == 0 {
		t.Fatalf("gemini-3-pro-high 应选 %d 或 %d，实际 %v", proOnly, unknown, got)
	}
}

func TestSelectReadsSnapshotNotStore(t *testing.T) {
	f := newFixture(t)
	if _, _, err := f.pick("gemini-2.5-flash", "medium"); err != ErrNoAccount {
		t.Fatalf("未加载时应返回 ErrNoAccount，实际 %v", err)
	}

	id := f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	if _, _, err := f.pick("gemini-2.5-flash", "medium"); err != ErrNoAccount {
		t.Fatalf("入库但未 Reload 时不应选到账号，实际 %v", err)
	}
	f.reload()
	if got, _, err := f.pick("gemini-2.5-flash", "medium"); err != nil || got != id {
		t.Fatalf("Reload 后应选到 %d，实际 %d %v", id, got, err)
	}

	if err := f.store.SetAccountStatus(context.Background(), id, "disabled", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.pick("gemini-2.5-flash", "medium"); err != nil {
		t.Fatalf("停用但未 Reload 时仍按旧快照选号，实际 %v", err)
	}
	f.reload()
	if _, _, err := f.pick("gemini-2.5-flash", "medium"); err != ErrNoAccount {
		t.Fatalf("停用并 Reload 后不应再选到，实际 %v", err)
	}
}

// 冷却按"账号 × 模型"生效：同族的其它模型、其它族都不受影响；只延长不缩短；到期恢复。
func TestCooldownByModel(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash", "gemini-3-pro-high")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-2.5-flash", "gemini-3-pro-high")
	f.reload()

	f.cooldown(a, "gemini-2.5-flash", time.Minute)
	if got := f.selectIDs("gemini-2.5-flash", 3); len(got) != 1 || got[b] != 3 {
		t.Fatalf("gemini-2.5-flash 应只选 %d，实际 %v", b, got)
	}
	if got := f.selectIDs("gemini-3-pro-high", 4); len(got) != 2 {
		t.Fatalf("同族的其它模型不应受影响，实际 %v", got)
	}

	f.cooldown(a, "gemini-2.5-flash", time.Second)
	if until := f.pool.cooldowns[cooldownKey{a, "gemini-2.5-flash"}].until; time.Until(until) < 30*time.Second {
		t.Fatalf("冷却被缩短到 %v", time.Until(until))
	}

	f.pool.cooldowns[cooldownKey{a, "gemini-2.5-flash"}] = cooldown{until: time.Now().Add(-time.Second)}
	if got := f.selectIDs("gemini-2.5-flash", 4); len(got) != 2 {
		t.Fatalf("冷却到期后应恢复轮询，实际 %v", got)
	}

	// 都在冷却：返回最早恢复的时间。
	f.cooldown(a, "gemini-2.5-flash", 10*time.Minute)
	f.cooldown(b, "gemini-2.5-flash", 2*time.Minute)
	_, _, err := f.pick("gemini-2.5-flash", "medium")
	var ue *UnavailableError
	if !errors.As(err, &ue) || !errors.Is(err, ErrNoAccount) || ue.Busy {
		t.Fatalf("全部冷却时应返回 UnavailableError（也是 ErrNoAccount），实际 %v", err)
	}
	if left := time.Until(ue.RetryAt); left < 110*time.Second || left > 2*time.Minute {
		t.Fatalf("RetryAt 应为最早恢复的 b（约 2 分钟后），实际 %v 后", left)
	}
}

// 冷却写库，新号池读回后仍然生效；过期的不读回。
func TestCooldownPersisted(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash", "claude-sonnet")
	f.reload()
	f.cooldown(a, "gemini-2.5-flash", time.Hour)

	restarted := New(f.store, f.enc)
	if err := restarted.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.LoadCooldowns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Select("gemini-2.5-flash", "medium", nil, 0); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("重启后冷却应仍生效，实际 %v", err)
	}
	if lease, err := restarted.Select("claude-sonnet", "medium", nil, 0); err != nil {
		t.Fatalf("其它模型不受影响：%v", err)
	} else {
		lease.Release()
	}
	got := restarted.Cooldowns()[a]
	if len(got) != 1 || got[0].Model != "gemini-2.5-flash" || got[0].Reason != "rate_limit" {
		t.Fatalf("Cooldowns 应列出这条冷却：%+v", got)
	}
}

// 家族名按各账号目录解析：只有能解析出来的账号参与选号，返回解析后的模型 ID。
func TestSelectResolvesFamilyPerAccount(t *testing.T) {
	f := newFixture(t)
	tiered := f.addAccount("a@example.com", "p-a", "enabled", "gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash-high")
	partial := f.addAccount("b@example.com", "p-b", "enabled", "gemini-3.8-flash-high")
	f.addAccount("c@example.com", "p-c", "enabled", "gemini-2.5-flash")
	f.reload()

	cases := []struct {
		tier string
		want map[int64]string // 账号 → 解析出的模型
	}{
		{"low", map[int64]string{tiered: "gemini-3.8-flash-low", partial: "gemini-3.8-flash-high"}},
		{"medium", map[int64]string{tiered: "gemini-3.8-flash-medium", partial: "gemini-3.8-flash-high"}},
		{"xhigh", map[int64]string{tiered: "gemini-3.8-flash-high", partial: "gemini-3.8-flash-high"}},
	}
	for _, tc := range cases {
		got := map[int64]string{}
		for i := 0; i < 4; i++ {
			id, model, err := f.pick("gemini-3.8-flash", tc.tier)
			if err != nil {
				t.Fatalf("%s：%v", tc.tier, err)
			}
			got[id] = model
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s：应只在能解析的账号间轮询，实际 %v", tc.tier, got)
		}
		for id, model := range tc.want {
			if got[id] != model {
				t.Fatalf("%s：账号 %d 应解析为 %s，实际 %v", tc.tier, id, model, got)
			}
		}
	}

	// 冷却按解析后的模型判断。
	f.cooldown(tiered, "gemini-3.8-flash-medium", time.Minute)
	if id, _, err := f.pick("gemini-3.8-flash", "medium"); err != nil || id != partial {
		t.Fatalf("冷却中的账号不应被选中：%d %v", id, err)
	}
	if id, model, err := f.pick("gemini-3.8-flash", "low"); err != nil || (id == tiered && model != "gemini-3.8-flash-low") {
		t.Fatalf("同账号其它档位不受影响：%d %s %v", id, model, err)
	}
}

func TestSelectModelNotFound(t *testing.T) {
	f := newFixture(t)
	if _, _, err := f.pick("gemini-9-flash", "medium"); err != ErrNoAccount {
		t.Fatalf("没有可调度账号时应返回 ErrNoAccount，实际 %v", err)
	}
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	f.reload()
	if _, _, err := f.pick("gemini-9-flash", "medium"); err != ErrModelNotFound {
		t.Fatalf("没有账号能解析时应返回 ErrModelNotFound，实际 %v", err)
	}
	f.cooldown(a, "gemini-2.5-flash", time.Minute)
	if _, _, err := f.pick("gemini-2.5-flash", "medium"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("能解析但都在冷却时应返回 ErrNoAccount，实际 %v", err)
	}
}

func TestLookupResolvesFamily(t *testing.T) {
	f := newFixture(t)
	f.addAccount("a@example.com", "p-a", "enabled", "claude-opus-5-5-low", "claude-opus-5-5-high")
	f.reload()
	if m, ok := f.pool.Lookup("claude-opus-5-5", "medium"); !ok || m.ID != "claude-opus-5-5-low" {
		t.Fatalf("medium 缺失时应先往低找，实际 %+v %v", m, ok)
	}
	if _, ok := f.pool.Lookup("claude-opus-9", "medium"); ok {
		t.Fatal("不存在的模型不应解析成功")
	}
}

// 并发上限：默认值和账号单独设置都生效；满了的账号跳过，全满时返回 Busy；归还后恢复。
func TestConcurrencyLimit(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-2.5-flash")
	if err := f.store.SetAccountMaxConcurrency(context.Background(), b, 1); err != nil {
		t.Fatal(err)
	}
	f.reload()
	f.pool.SetDefaultConcurrency(2)

	var leases []*Lease
	for range 3 {
		l, err := f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
		if err != nil {
			t.Fatalf("前 3 个请求应有名额（a 2 个、b 1 个）：%v", err)
		}
		leases = append(leases, l)
	}
	if f.pool.InFlight(a) != 2 || f.pool.InFlight(b) != 1 {
		t.Fatalf("a 应占 2 个、b 占 1 个，实际 %d、%d", f.pool.InFlight(a), f.pool.InFlight(b))
	}
	_, err := f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
	var ue *UnavailableError
	if !errors.As(err, &ue) || !ue.Busy || !ue.RetryAt.IsZero() {
		t.Fatalf("全满时应返回 Busy，实际 %v", err)
	}
	leases[0].Release()
	leases[0].Release() // 重复归还无副作用
	l, err := f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
	if err != nil || l.Account.ID != leases[0].Account.ID {
		t.Fatalf("归还后应能再选到同一账号：%v %v", l, err)
	}
	if got := f.pool.InFlight(leases[0].Account.ID); got != f.pool.Limit(leases[0].Account.Account) {
		t.Fatalf("重复归还不应多减：%d", got)
	}
}

// InFlightAll 取所有账号当前并发的快照：占用为 0 的不在表里，返回的是副本（改它不影响号池）。
func TestInFlightAll(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-2.5-flash")
	f.reload()
	f.pool.SetDefaultConcurrency(5)

	if got := f.pool.InFlightAll(); len(got) != 0 {
		t.Fatalf("没有进行中的请求时应为空，实际 %v", got)
	}

	var leases []*Lease
	for range 2 {
		l, err := f.pool.Select("gemini-2.5-flash", "medium", map[int64]bool{b: true}, 0) // 固定选 a
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, l)
	}
	l, err := f.pool.Select("gemini-2.5-flash", "medium", map[int64]bool{a: true}, 0) // 选 b
	if err != nil {
		t.Fatal(err)
	}
	leases = append(leases, l)

	got := f.pool.InFlightAll()
	if got[a] != 2 || got[b] != 1 {
		t.Fatalf("a 应占 2、b 占 1，实际 %v", got)
	}
	// 快照是副本：改它不影响号池。
	got[a] = 99
	if f.pool.InFlight(a) != 2 {
		t.Fatalf("修改快照不应影响号池，实际 %d", f.pool.InFlight(a))
	}

	for _, l := range leases {
		l.Release()
	}
	if got := f.pool.InFlightAll(); len(got) != 0 {
		t.Fatalf("全部归还后应为空，实际 %v", got)
	}
}

// Acquire 在并发全满时等名额释放；等不到时返回 Busy；冷却（而不是并发满）时不等。
func TestAcquireWaitsForSlot(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	f.reload()
	f.pool.SetDefaultConcurrency(1)
	held, err := f.pool.Select("gemini-2.5-flash", "medium", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		held.Release()
	}()
	start := time.Now()
	l, err := f.pool.Acquire(context.Background(), "gemini-2.5-flash", "medium", nil, 0, false, 2*time.Second)
	if err != nil || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("应等到名额释放后选到：%v（等了 %v）", err, time.Since(start))
	}
	start = time.Now()
	if _, err := f.pool.Acquire(context.Background(), "gemini-2.5-flash", "medium", nil, 0, false, 100*time.Millisecond); !errors.Is(err, ErrNoAccount) || time.Since(start) < 90*time.Millisecond {
		t.Fatalf("等不到时应在超时后返回：%v（等了 %v）", err, time.Since(start))
	}
	l.Release()

	f.cooldown(a, "gemini-2.5-flash", time.Minute)
	start = time.Now()
	if _, err := f.pool.Acquire(context.Background(), "gemini-2.5-flash", "medium", nil, 0, false, time.Second); !errors.Is(err, ErrNoAccount) || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("只是冷却时不应等：%v（等了 %v）", err, time.Since(start))
	}
}

// exclude 里的账号不再选；全被排除时返回不带 RetryAt、不 Busy 的 UnavailableError。
func TestSelectExclude(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-2.5-flash")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-2.5-flash")
	f.reload()
	for range 3 {
		l, err := f.pool.Select("gemini-2.5-flash", "medium", map[int64]bool{a: true}, 0)
		if err != nil || l.Account.ID != b {
			t.Fatalf("排除 a 后应只选 b：%v %v", l, err)
		}
		l.Release()
	}
	_, err := f.pool.Select("gemini-2.5-flash", "medium", map[int64]bool{a: true, b: true}, 0)
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Busy || !ue.RetryAt.IsZero() {
		t.Fatalf("全被排除时应返回空的 UnavailableError，实际 %v", err)
	}
}

// 瞬时限流连续次数：累加，成功清零，隔太久从头算。
func TestRateLimitStrikes(t *testing.T) {
	f := newFixture(t)
	for want := 1; want <= 3; want++ {
		if got := f.pool.NoteRateLimit(7); got != want {
			t.Fatalf("第 %d 次应返回 %d，实际 %d", want, want, got)
		}
	}
	f.pool.NoteSuccess(7)
	if got := f.pool.NoteRateLimit(7); got != 1 {
		t.Fatalf("成功后应从 1 算，实际 %d", got)
	}
	f.pool.strikes[7] = strike{count: 5, last: time.Now().Add(-strikeReset - time.Second)}
	if got := f.pool.NoteRateLimit(7); got != 1 {
		t.Fatalf("隔太久应从 1 算，实际 %d", got)
	}
}

// 会话粘性：优先账号能接就一直选它；不能接时换别的账号，Affinity 记下原因。
func TestPreferAccount(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-3-flash")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-3-flash")
	f.reload()
	sel := func(prefer int64, exclude map[int64]bool) *Lease {
		t.Helper()
		l, err := f.pool.Select("gemini-3-flash", "medium", exclude, prefer)
		if err != nil {
			t.Fatal(err)
		}
		l.Release()
		return l
	}
	for range 4 {
		if l := sel(b, nil); l.Account.ID != b || l.Affinity != AffinityHit {
			t.Fatalf("prefer b: %d %q", l.Account.ID, l.Affinity)
		}
	}
	if l := sel(0, nil); l.Affinity != "" {
		t.Fatalf("no prefer: %q", l.Affinity)
	}
	if l := sel(b, map[int64]bool{b: true}); l.Account.ID != a || l.Affinity != AffinityFailed {
		t.Fatalf("failed: %d %q", l.Account.ID, l.Affinity)
	}
	if l := sel(999, nil); l.Affinity != AffinityUnavailable {
		t.Fatalf("unknown account: %q", l.Affinity)
	}

	f.cooldown(b, "gemini-3-flash", time.Minute)
	if l := sel(b, nil); l.Account.ID != a || l.Affinity != AffinityCooldown {
		t.Fatalf("cooldown: %d %q", l.Account.ID, l.Affinity)
	}
	f.reload()
	f.pool.cooldowns = map[cooldownKey]cooldown{}

	f.pool.SetDefaultConcurrency(1)
	held, err := f.pool.Select("gemini-3-flash", "medium", map[int64]bool{a: true}, 0)
	if err != nil || held.Account.ID != b {
		t.Fatalf("hold b: %v", err)
	}
	if l := sel(b, nil); l.Account.ID != a || l.Affinity != AffinityBusy {
		t.Fatalf("busy: %d %q", l.Account.ID, l.Affinity)
	}
	held.Release()
	f.pool.SetDefaultConcurrency(DefaultConcurrency)

	f.pool.SetLowQuota(0.1)
	f.setQuota(b, map[string]*float64{"gemini-3-flash": frac(0.05)})
	f.setQuota(a, map[string]*float64{"gemini-3-flash": frac(0.8)})
	if l := sel(b, nil); l.Account.ID != a || l.Affinity != AffinityLowQuota {
		t.Fatalf("low quota: %d %q", l.Account.ID, l.Affinity)
	}
	f.setQuota(a, map[string]*float64{"gemini-3-flash": frac(0.05)})
	if l := sel(b, nil); l.Account.ID != b || l.Affinity != AffinityHit {
		t.Fatalf("all low quota keeps preferred: %d %q", l.Account.ID, l.Affinity)
	}

	f.pool.Pause(b, time.Now().Add(time.Minute))
	if l := sel(b, nil); l.Account.ID != a || l.Affinity != AffinityPaused {
		t.Fatalf("paused: %d %q", l.Account.ID, l.Affinity)
	}
}

// 上游的占位条目（<家族>-disabled）不是这个账号的模型：只有占位条目的账号选不到这个家族，也不当成"目录未知"放行；
// 占位条目自己既解析不到，也不进模型汇总。
func TestUnavailablePlaceholder(t *testing.T) {
	f := newFixture(t)
	f.addAccount("a@example.com", "p-a", "enabled", "gemini-4-carbon-disabled", "gemini-3-flash")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-4-carbon-low", "gemini-4-carbon-high")
	only := f.addAccount("c@example.com", "p-c", "enabled", "gemini-4-carbon-disabled")
	f.reload()

	for range 4 {
		id, model, err := f.pick("gemini-4-carbon", "high")
		if err != nil || id != b || model != "gemini-4-carbon-high" {
			t.Fatalf("Carbon 只应选到 b：id=%d model=%s err=%v", id, model, err)
		}
	}
	if id, _, err := f.pick("gemini-3-flash", "medium"); err != nil || id == only {
		t.Fatalf("只有占位条目的账号不应当成目录未知放行：id=%d err=%v", id, err)
	}
	if _, ok := f.pool.Lookup("gemini-4-carbon-disabled", "medium"); ok {
		t.Fatal("占位条目不应解析成模型")
	}
	for _, m := range f.pool.Models() {
		if m.ID == "gemini-4-carbon-disabled" {
			t.Fatal("占位条目不应进模型汇总")
		}
	}
}
