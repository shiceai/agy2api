package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/store"
)

func (f *fakeUpstream) counts() (fetch, load int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetchCalls, f.loadCalls
}

func TestRefreshQuotas(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "rt-a")
	e.setCatalog("rt-a", "m1", "m2")
	e.up.catalogs["rt-a"] = []string{"m1", "m2"}
	e.up.remaining["rt-a"] = 0.4
	e.up.paidTier["rt-a"] = "Google AI Pro"
	id := e.ids["rt-a"]

	// 撞了额度耗尽的冷却，随后刷新到的额度已恢复（剩余不到 24 小时），冷却提前解除。
	e.relay.pool.Cooldown(ctx, id, time.Now().Add(2*time.Hour), string(antigravity.KindQuotaExhausted), "m1")
	time.Sleep(5 * time.Millisecond)

	q := &quotaRefresher{models: map[int64]time.Time{}, subs: map[int64]time.Time{}}
	e.relay.refreshQuotas(ctx, q)
	if fetch, load := e.up.counts(); fetch != 1 || load != 1 {
		t.Fatalf("calls: fetch %d load %d, want 1 and 1", fetch, load)
	}
	a, err := e.store.GetAccount(ctx, e.enc, id)
	if err != nil {
		t.Fatal(err)
	}
	models := antigravity.UnmarshalModels(a.ModelsJSON)
	if len(models) != 2 || models[0].RemainingFraction == nil || *models[0].RemainingFraction != 0.4 {
		t.Fatalf("models not refreshed: %+v", models)
	}
	if a.ModelsRefreshedAt == 0 || a.SubscriptionCheckedAt == 0 {
		t.Fatalf("refresh times not set: %d %d", a.ModelsRefreshedAt, a.SubscriptionCheckedAt)
	}
	if sub := antigravity.UnmarshalSubscription(a.SubscriptionJSON); sub.PaidTier != "Google AI Pro" || sub.Downgraded() {
		t.Fatalf("subscription: %+v", sub)
	}
	if cds := e.relay.Cooldowns()[id]; len(cds) != 0 {
		t.Fatalf("quota cooldown not cleared: %+v", cds)
	}
	// 号池已重新加载，选号能看到新额度。
	if rem, ok := e.relay.pool.Accounts()[0].Model("m2"); !ok || rem.RemainingFraction == nil {
		t.Fatalf("pool not reloaded: %+v", rem)
	}

	// 没到间隔不再请求上游。
	e.relay.refreshQuotas(ctx, q)
	if fetch, load := e.up.counts(); fetch != 1 || load != 1 {
		t.Fatalf("refreshed again before interval: fetch %d load %d", fetch, load)
	}

	// 默认不拉每周额度；账号单独开启后，刷新目录时顺带拉。
	if e.up.summaryCalls != 0 || a.QuotaSummaryJSON != "" {
		t.Fatalf("默认不应拉每周额度：%d %q", e.up.summaryCalls, a.QuotaSummaryJSON)
	}
	if err := e.store.SetAccountWeeklyQuota(ctx, id, store.WeeklyQuotaOn); err != nil {
		t.Fatal(err)
	}
	if err := e.relay.pool.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.relay.refreshAccountQuota(ctx, e.relay.pool.Accounts()[0], true, false) {
		t.Fatal("刷新应有写库")
	}
	a, err = e.store.GetAccount(ctx, e.enc, id)
	if err != nil {
		t.Fatal(err)
	}
	if s := antigravity.UnmarshalQuotaSummary(a.QuotaSummaryJSON); e.up.summaryCalls != 1 || s == nil || len(s.Groups) != 1 ||
		*s.Groups[0].Buckets[0].RemainingFraction != 0.3 || a.QuotaSummaryAt == 0 {
		t.Fatalf("开启后应拉到每周额度：%d %q", e.up.summaryCalls, a.QuotaSummaryJSON)
	}

	// 间隔设为 0 时关闭。
	before, _ := e.up.counts()
	p := e.relay.Policy()
	p.QuotaRefresh = 0
	e.relay.SetPolicy(p)
	e.relay.refreshQuotas(ctx, &quotaRefresher{models: map[int64]time.Time{}, subs: map[int64]time.Time{}})
	if fetch, _ := e.up.counts(); fetch != before {
		t.Fatalf("refreshed while disabled: fetch %d, before %d", fetch, before)
	}
}

func TestRefreshQuotasFailureWaitsInterval(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "rt-a")
	e.up.revoked["rt-a"] = true // 取令牌失败（并会停用账号）
	q := &quotaRefresher{models: map[int64]time.Time{}, subs: map[int64]time.Time{}}
	e.relay.refreshQuotas(ctx, q)
	if q.models[e.ids["rt-a"]].IsZero() {
		t.Fatal("failed attempt not recorded")
	}
}

func TestStagger(t *testing.T) {
	interval := 15 * time.Minute
	seen := map[time.Duration]bool{}
	for id := int64(1); id <= 20; id++ {
		d := stagger(interval, id)
		if d < interval*9/10 || d > interval*11/10 {
			t.Fatalf("stagger(%d) = %v out of ±10%%", id, d)
		}
		if stagger(interval, id) != d {
			t.Fatal("stagger not deterministic")
		}
		seen[d] = true
	}
	if len(seen) < 15 {
		t.Fatalf("stagger not spread: %d distinct of 20", len(seen))
	}
}

// 一个模型额度耗尽时，同一额度窗口（剩余比例和重置时间相同）的其它模型一起冷却。
func TestQuotaExhaustedCoolsSharedWindow(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "rt-a")
	half, full := 0.5, 1.0
	catalog := []antigravity.ModelInfo{
		{ID: "m1", RemainingFraction: &half, ResetTime: "2026-11-23T10:00:00Z"},
		{ID: "m2", RemainingFraction: &half, ResetTime: "2026-11-23T10:00:00Z"},
		{ID: "m3", RemainingFraction: &full, ResetTime: "2026-11-29T10:00:00Z"},
	}
	if err := e.store.UpdateAccountModels(ctx, e.ids["rt-a"], antigravity.MarshalModels(catalog)); err != nil {
		t.Fatal(err)
	}
	if err := e.relay.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	e.up.script["rt-a"] = []int{429}
	if _, _, _, err := e.generate("m1", false); err == nil {
		t.Fatal("want error after quota exhausted")
	}
	got := map[string]string{}
	for _, c := range e.relay.Cooldowns()[e.ids["rt-a"]] {
		got[c.Model] = c.Reason
	}
	quota := string(antigravity.KindQuotaExhausted)
	if got["m1"] != quota || got["m2"] != quota || got["m3"] != "" {
		t.Fatalf("cooldowns: %v", got)
	}
	// 同窗口的冷却一起落库，重启后读回。
	rows, err := e.store.ActiveCooldowns(ctx, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]string{}
	for _, c := range rows {
		stored[c.Model] = c.Reason
	}
	if stored["m1"] != quota || stored["m2"] != quota || len(stored) != 2 {
		t.Fatalf("stored cooldowns: %v", stored)
	}
}

// 绑了代理的账号，控制面请求（刷新模型目录、后台刷新额度和订阅）也都经代理出网，不从本机直连上游。
func TestControlPlaneUsesAccountProxy(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "a")
	var mu sync.Mutex
	viaProxy := map[string]int{}
	// 普通 HTTP 代理：请求行是完整地址，这里计数后直接交给假上游处理。
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		viaProxy[r.URL.Path]++
		mu.Unlock()
		e.up.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	p := &store.Proxy{Name: "p", URL: proxy.URL}
	if err := e.store.CreateProxy(ctx, e.enc, p); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetAccountsProxy(ctx, map[int64]int64{e.ids["a"]: p.ID}); err != nil {
		t.Fatal(err)
	}
	if err := e.relay.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	e.up.catalogs["a"] = []string{"m1"}

	// 目录为空、没有账号能解析请求的模型：先刷新目录。
	if _, ok := e.relay.LookupModel(ctx, "m1"); !ok {
		t.Fatal("刷新目录后应能解析 m1")
	}
	// 后台刷新额度和订阅。
	accounts := e.relay.pool.Accounts()
	if len(accounts) != 1 || accounts[0].ProxyURL == "" {
		t.Fatalf("账号应绑着代理：%+v", accounts)
	}
	e.relay.refreshAccountQuota(ctx, accounts[0], true, true)

	e.up.mu.Lock()
	fetch, load := e.up.fetchCalls, e.up.loadCalls
	e.up.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if fetch < 2 || viaProxy["/v1internal:fetchAvailableModels"] != fetch {
		t.Fatalf("取模型目录应全部经代理：共 %d 次，经代理 %d 次", fetch, viaProxy["/v1internal:fetchAvailableModels"])
	}
	if load < 1 || viaProxy["/v1internal:loadCodeAssist"] != load {
		t.Fatalf("查订阅应全部经代理：共 %d 次，经代理 %d 次", load, viaProxy["/v1internal:loadCodeAssist"])
	}
}

// 刷新模型目录时各账号并发拉取：发起请求的客户端在等，逐个拉取时账号一多就超时。
func TestRefreshCatalogsInParallel(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "a", "b", "c", "d")
	for _, rt := range []string{"a", "b", "c", "d"} {
		e.up.catalogs[rt] = []string{"m1"}
	}
	e.up.fetchDelay = 300 * time.Millisecond
	start := time.Now()
	if _, ok := e.relay.LookupModel(ctx, "m1"); !ok {
		t.Fatal("刷新目录后应能解析 m1")
	}
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("4 个账号各要 300ms，并发拉取应远小于逐个的 1.2s，实际 %s", elapsed)
	}
	for _, a := range e.relay.pool.Accounts() {
		if _, ok := a.Model("m1"); !ok {
			t.Fatalf("账号 %d 的目录应已更新", a.ID)
		}
	}
}
