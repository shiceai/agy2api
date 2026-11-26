package pool

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
)

// setReset 把账号某模型的剩余额度和重置时间写进目录，然后重新加载。
func (f *fixture) setReset(id int64, model string, remaining float64, reset time.Time) {
	f.t.Helper()
	catalog := []antigravity.ModelInfo{{ID: model, RemainingFraction: frac(remaining), ResetTime: reset.UTC().Format(time.RFC3339)}}
	if err := f.store.UpdateAccountModels(context.Background(), id, antigravity.MarshalModels(catalog)); err != nil {
		f.t.Fatalf("update models: %v", err)
	}
	f.reload()
}

// 失败率按 alpha 计入，不再有结果时每 15 分钟减半；首字延迟取平滑值。
func TestHealthDecay(t *testing.T) {
	now := time.Now()
	h := &health{}
	h.note(true, 0, now)
	if math.Abs(h.failureAt(now)-healthAlpha) > 1e-9 {
		t.Fatalf("一次失败后应为 %v：%v", healthAlpha, h.failureAt(now))
	}
	if got := h.failureAt(now.Add(healthHalfLife)); math.Abs(got-healthAlpha/2) > 1e-9 {
		t.Fatalf("15 分钟后应减半：%v", got)
	}
	h.note(false, 2*time.Second, now)
	h.note(false, 4*time.Second, now)
	if want := 0.2*4000 + 0.8*2000; math.Abs(h.latencyMs-want) > 1e-6 {
		t.Fatalf("首字延迟平滑值应为 %v：%v", want, h.latencyMs)
	}
}

// 智能：近期失败多、额度少、延迟高、负载高的账号分数低，少选；分数接近的轮流。
func TestSmartStrategy(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-3-flash")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-3-flash")
	f.reload()
	f.pool.SetStrategy(StrategySmart)

	if got := f.selectIDs("gemini-3-flash", 4); got[a] != 2 || got[b] != 2 {
		t.Fatalf("分数相同应轮流：%v", got)
	}
	for range 3 {
		f.pool.NoteFailure(a)
	}
	if got := f.selectIDs("gemini-3-flash", 4); got[b] != 4 {
		t.Fatalf("近期失败多的 a 应少选：%v", got)
	}

	g := newFixture(t)
	c := g.addAccount("c@example.com", "p-c", "enabled", "gemini-3-flash")
	d := g.addAccount("d@example.com", "p-d", "enabled", "gemini-3-flash")
	g.setQuota(c, map[string]*float64{"gemini-3-flash": frac(0.2)})
	g.setQuota(d, map[string]*float64{"gemini-3-flash": frac(0.9)})
	g.pool.SetStrategy(StrategySmart)
	if got := g.selectIDs("gemini-3-flash", 4); got[d] != 4 {
		t.Fatalf("额度多的 d 应优先：%v", got)
	}
	// d 的首字延迟 60 秒：延迟分 0.14，比 c 低 0.43，盖过额度的差（0.56）还差一点，仍选 d；再加上负载就轮到 c。
	g.pool.NoteSuccess(d, 60*time.Second)
	if got := g.selectIDs("gemini-3-flash", 2); got[d] != 2 {
		t.Fatalf("额度差更大时仍选 d：%v", got)
	}
	g.pool.SetDefaultConcurrency(2)
	held, err := g.pool.Select("gemini-3-flash", "medium", map[int64]bool{c: true}, 0)
	if err != nil || held.Account.ID != d {
		t.Fatalf("应占住 d：%v %v", held, err)
	}
	if id, _, err := g.pick("gemini-3-flash", "medium"); err != nil || id != c {
		t.Fatalf("d 一半名额被占、延迟又高时应选 c：%d %v", id, err)
	}
	held.Release()
}

// 重置优先：先用额度最早重置的账号；没用过的窗口（剩 100%）排在后面；同时重置的选负载低的；都不知道时轮流。
func TestResetFirstStrategy(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount("a@example.com", "p-a", "enabled", "gemini-3-flash")
	b := f.addAccount("b@example.com", "p-b", "enabled", "gemini-3-flash")
	c := f.addAccount("c@example.com", "p-c", "enabled", "gemini-3-flash")
	now := time.Now()
	f.setReset(a, "gemini-3-flash", 0.5, now.Add(3*time.Hour))
	f.setReset(b, "gemini-3-flash", 0.7, now.Add(time.Hour))
	f.setReset(c, "gemini-3-flash", 1, now.Add(30*time.Minute)) // 没用过：重置时间不算
	f.pool.SetStrategy(StrategyResetFirst)

	if got := f.selectIDs("gemini-3-flash", 3); got[b] != 3 {
		t.Fatalf("应一直选最早重置的 b：%v", got)
	}
	// b 并发满了：选下一个重置的 a，而不是没用过的 c。
	f.pool.SetDefaultConcurrency(1)
	held, err := f.pool.Select("gemini-3-flash", "medium", nil, 0)
	if err != nil || held.Account.ID != b {
		t.Fatalf("应先选 b：%v %v", held, err)
	}
	if id, _, err := f.pick("gemini-3-flash", "medium"); err != nil || id != a {
		t.Fatalf("b 满了应选 a：%d %v", id, err)
	}
	held.Release()

	// a、b 在同一分钟内重置：选负载低的。
	f.setReset(a, "gemini-3-flash", 0.5, now.Add(time.Hour+20*time.Second))
	f.pool.SetDefaultConcurrency(4)
	held, err = f.pool.Select("gemini-3-flash", "medium", map[int64]bool{a: true, c: true}, 0)
	if err != nil || held.Account.ID != b {
		t.Fatalf("应占住 b：%v %v", held, err)
	}
	if id, _, err := f.pick("gemini-3-flash", "medium"); err != nil || id != a {
		t.Fatalf("同时重置时应选负载低的 a：%d %v", id, err)
	}
	held.Release()

	// 都不知道重置时间（都没用过）：轮流。
	g := newFixture(t)
	x := g.addAccount("x@example.com", "p-x", "enabled", "gemini-3-flash")
	y := g.addAccount("y@example.com", "p-y", "enabled", "gemini-3-flash")
	g.reload()
	g.pool.SetStrategy(StrategyResetFirst)
	if got := g.selectIDs("gemini-3-flash", 4); got[x] != 2 || got[y] != 2 {
		t.Fatalf("都不知道重置时间时应轮流：%v", got)
	}
}
