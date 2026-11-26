package relay

import (
	"context"
	"log/slog"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/pool"
	"github.com/shiceai/agy2api/internal/store"
)

// 后台额度刷新：按 Policy.QuotaRefresh 的间隔给每个启用账号拉一次模型目录（含单模型剩余额度），
// 每 subscriptionInterval 查一次订阅档位。默认只调官方客户端实抓里出现过的 fetchAvailableModels、loadCodeAssist；
// 开了每周额度的账号拉目录时另调 retrieveUserQuotaSummary（见 WeeklyQuotaOn）。
// 刷新后重新加载号池，选号按新额度避让，并解除已经恢复的额度耗尽冷却。
const (
	quotaTick            = time.Minute
	subscriptionInterval = 6 * time.Hour
	quotaCallTimeout     = 30 * time.Second
	// quotaAccountGap 是同一轮里两个账号之间的间隔，避免一起打到上游。
	quotaAccountGap = 2 * time.Second
)

// quotaRefresher 是后台刷新协程的状态，只在协程里访问。
type quotaRefresher struct {
	cancel context.CancelFunc
	done   chan struct{}
	// models、subs 记每个账号上次尝试的时间（失败也算）：失败后等满一个间隔再试，不每分钟重试。
	models map[int64]time.Time
	subs   map[int64]time.Time
}

// StartQuotaRefresh 启动后台额度刷新，Close 时停止。只调用一次，在 Reload 之后。
func (r *Relay) StartQuotaRefresh() {
	ctx, cancel := context.WithCancel(context.Background())
	q := &quotaRefresher{cancel: cancel, done: make(chan struct{}), models: map[int64]time.Time{}, subs: map[int64]time.Time{}}
	r.quota = q
	go func() {
		defer close(q.done)
		t := time.NewTicker(quotaTick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.refreshQuotas(ctx, q)
			}
		}
	}()
}

// stopQuotaRefresh 停止后台刷新并等它退出，ctx 到期则不再等。
func (r *Relay) stopQuotaRefresh(ctx context.Context) {
	q := r.quota
	if q == nil {
		return
	}
	q.cancel()
	select {
	case <-q.done:
	case <-ctx.Done():
	}
}

// refreshQuotas 刷新到期的账号。间隔为 0 时不刷新。
func (r *Relay) refreshQuotas(ctx context.Context, q *quotaRefresher) {
	interval := r.Policy().QuotaRefresh
	if interval <= 0 {
		return
	}
	changed, first := false, true
	for _, a := range r.pool.Accounts() {
		if a.ProjectID == "" {
			continue
		}
		now := time.Now()
		models := due(now, a.ModelsRefreshedAt, q.models[a.ID], stagger(interval, a.ID))
		sub := due(now, a.SubscriptionCheckedAt, q.subs[a.ID], stagger(subscriptionInterval, a.ID))
		if !models && !sub {
			continue
		}
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-time.After(quotaAccountGap):
			}
		}
		first = false
		if models {
			q.models[a.ID] = now
		}
		if sub {
			q.subs[a.ID] = now
		}
		if r.refreshAccountQuota(ctx, a, models, sub) {
			changed = true
		}
		if ctx.Err() != nil {
			return
		}
	}
	if changed {
		r.reloadAndReconcile(ctx)
	}
}

// refreshAccountQuota 刷新一个账号的模型目录和（或）订阅信息，返回是否有写库。
func (r *Relay) refreshAccountQuota(ctx context.Context, a *pool.Account, models, sub bool) bool {
	ctx, cancel := context.WithTimeout(ctx, quotaCallTimeout)
	defer cancel()
	log := slog.With("account_id", a.ID)
	token, err := r.accessToken(ctx, a)
	if err != nil {
		log.WarnContext(ctx, "后台刷新额度失败：取令牌出错", "err", err)
		return false
	}
	cred := controlCredential(a, token)
	changed := false
	if models && r.WeeklyQuotaOn(a.Account) && r.refreshQuotaSummary(ctx, a.ID, cred) {
		changed = true
	}
	if models {
		list, err := r.client.FetchAvailableModels(ctx, cred)
		switch {
		case err != nil:
			log.WarnContext(ctx, "后台刷新额度失败", "err", err)
			if antigravity.IsUnauthorized(err) {
				r.tokens.Invalidate(a.ID)
			}
		case len(list) == 0:
			// 空目录会让账号变成"放行任何模型"，不覆盖
		default:
			if err := r.store.UpdateAccountModels(ctx, a.ID, antigravity.MarshalModels(list)); err != nil {
				log.ErrorContext(ctx, "保存模型目录失败", "err", err)
			} else {
				log.DebugContext(ctx, "已刷新额度", "models", len(list))
				changed = true
			}
		}
	}
	if sub {
		s, err := r.client.FetchSubscription(ctx, cred)
		if err != nil {
			log.WarnContext(ctx, "检查订阅失败", "err", err)
			return changed
		}
		old := antigravity.UnmarshalSubscription(a.SubscriptionJSON)
		if err := r.store.SetAccountSubscription(ctx, a.ID, antigravity.MarshalSubscription(s)); err != nil {
			log.ErrorContext(ctx, "保存订阅信息失败", "err", err)
			return changed
		}
		changed = true
		log.DebugContext(ctx, "已检查订阅", "paid_tier", s.PaidTier)
		switch {
		case s.Downgraded() && !old.Downgraded():
			log.WarnContext(ctx, "账号付费档位降到 Starter，订阅可能已失效", "paid_tier", s.PaidTier, "previous", old.PaidTier)
		case s.PaidTier != old.PaidTier:
			log.InfoContext(ctx, "账号付费档位变化", "paid_tier", s.PaidTier, "previous", old.PaidTier)
		}
	}
	return changed
}

// WeeklyQuotaOn 报告账号要不要拉每周额度：账号自己设了开、关就按它，否则跟随全局设置。
func (r *Relay) WeeklyQuotaOn(a *store.Account) bool {
	switch a.WeeklyQuota {
	case store.WeeklyQuotaOn:
		return true
	case store.WeeklyQuotaOff:
		return false
	}
	return r.Policy().WeeklyQuota
}

// refreshQuotaSummary 拉一个账号的分组额度（每周额度）并落库，返回是否写入。失败只记日志，不影响账号状态。
func (r *Relay) refreshQuotaSummary(ctx context.Context, id int64, cred *antigravity.Credential) bool {
	log := slog.With("account_id", id)
	summary, err := r.client.RetrieveQuotaSummary(ctx, cred)
	if err != nil {
		log.WarnContext(ctx, "拉取每周额度失败", "err", err)
		return false
	}
	if err := r.store.SetAccountQuotaSummary(ctx, id, antigravity.MarshalQuotaSummary(summary)); err != nil {
		log.ErrorContext(ctx, "保存每周额度失败", "err", err)
		return false
	}
	log.DebugContext(ctx, "已拉取每周额度", "groups", len(summary.Groups))
	return true
}

// SyncQuotaSummary 在手动同步账号后拉一次分组额度（账号没开每周额度时不拉），返回是否写入。
func (r *Relay) SyncQuotaSummary(ctx context.Context, a *store.Account) bool {
	if !r.WeeklyQuotaOn(a) {
		return false
	}
	pa := pool.NewAccount(a)
	token, err := r.accessToken(ctx, pa)
	if err != nil {
		slog.WarnContext(ctx, "拉取每周额度失败：取令牌出错", "account_id", a.ID, "err", err)
		return false
	}
	return r.refreshQuotaSummary(ctx, a.ID, controlCredential(pa, token))
}

// reloadAndReconcile 在模型目录（额度）落库后重新加载号池，并解除已经恢复的额度耗尽冷却。
func (r *Relay) reloadAndReconcile(ctx context.Context) {
	if err := r.pool.Reload(ctx); err != nil {
		slog.ErrorContext(ctx, "刷新号池失败", "err", err)
		return
	}
	r.pool.ReconcileCooldowns(ctx)
}

// due 判断距上次（库里记的时间和本进程上次尝试的时间取较晚者）是否已过 every。
func due(now time.Time, lastMs int64, attempted time.Time, every time.Duration) bool {
	last := time.UnixMilli(lastMs)
	if attempted.After(last) {
		last = attempted
	}
	return now.Sub(last) >= every
}

// stagger 按账号 ID 把间隔固定地错开 ±10%，同时导入的账号不会每次都挤在同一分钟刷新。
func stagger(interval time.Duration, accountID int64) time.Duration {
	h := uint64(accountID) * 0x9E3779B97F4A7C15
	frac := float64(h>>11) / float64(1<<53) // [0, 1)
	return time.Duration(float64(interval) * (0.9 + 0.2*frac))
}
