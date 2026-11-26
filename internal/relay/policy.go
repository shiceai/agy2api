package relay

import (
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/pool"
)

// Policy 是管理端可调的调度与容错设置，整体替换、立即生效（见 Relay.SetPolicy）。
// 判断上游返回了什么、怎么守住账号安全的规则不在这里，写死在 decide 里。
type Policy struct {
	// TokenRefresh 决定 access token 提前多久刷新。
	TokenRefresh antigravity.RefreshPolicy
	// Concurrency 是每个账号默认的并发上限，账号可单独设置。
	Concurrency int
	// SlotWait 是支持该模型的账号并发都满时，等名额的最长时间。
	SlotWait time.Duration
	// MaxAttempts 是单次请求最多尝试的账号数。
	MaxAttempts int
	// CooldownWait 是单次请求最多等冷却的时间：选不到账号、而最早恢复的账号在剩余时间内就能用时，先等到那时再选一轮
	// （试过的账号也重新参与），不立即返回 429；0 表示不等。等待之后的尝试仍算在 MaxAttempts 里。
	CooldownWait time.Duration
	// RateLimitBase、RateLimitMax：瞬时限流（上游没给恢复时间）的冷却从 Base 起，
	// 同一账号连续触发就翻倍，最多 Max。
	RateLimitBase time.Duration
	RateLimitMax  time.Duration
	// ServerErrorCooldown 是上游 5xx 后该账号这个模型的休息时间，0 表示不休息。
	ServerErrorCooldown time.Duration
	// ValidationCooldown 是 403 需要验证后该账号这个模型的冷却。
	ValidationCooldown time.Duration
	// ModelForbiddenCooldown 是 403 无权使用该模型后的冷却。
	ModelForbiddenCooldown time.Duration
	// QuotaRefresh 是后台刷新各账号模型额度的间隔，0 表示不刷新（只在同步时更新）。
	QuotaRefresh time.Duration
	// LowQuota 是避让阈值（0～1 的比例）：账号对请求的模型剩余额度低于它时先选别的账号，0 表示不避让。
	LowQuota float64
	// StreamIdleTimeout 是流式响应中途两次从上游读到数据的最长间隔，超过就断开；0 表示不断开。
	StreamIdleTimeout time.Duration
	// RequireProxy 为 true 时只用绑定了代理的账号，没绑的不参与调度。
	RequireProxy bool
	// Sticky 开启会话粘性：同一对话优先用上次的账号；StickyTTL 是对话闲置多久后不再粘。见 affinity.go。
	Sticky    bool
	StickyTTL time.Duration
	// WeeklyQuota 是每周额度的全局开关：开启后后台刷新额度时顺带拉分组额度（retrieveUserQuotaSummary），
	// 账号可以单独开、关（store.Account.WeeklyQuota）。默认关：官方客户端抓包里没见过这个请求。
	WeeklyQuota bool
}

// DefaultPolicy 是未配置时的设置。5xx 休息时间按小号池取得较短：一两个号的池子不能因为一次 500
// 整体停很久。等冷却默认 30 秒：容量不足（10 秒）、5xx 休息（30 秒）这类短冷却等得到，瞬时限流（60 秒起）和额度耗尽不等。
func DefaultPolicy() Policy {
	return Policy{
		TokenRefresh:           antigravity.DefaultRefreshPolicy(),
		Concurrency:            pool.DefaultConcurrency,
		SlotWait:               30 * time.Second,
		MaxAttempts:            3,
		CooldownWait:           30 * time.Second,
		RateLimitBase:          60 * time.Second,
		RateLimitMax:           300 * time.Second,
		ServerErrorCooldown:    30 * time.Second,
		ValidationCooldown:     10 * time.Minute,
		ModelForbiddenCooldown: 5 * time.Minute,
		QuotaRefresh:           15 * time.Minute,
		LowQuota:               0.1,
		StreamIdleTimeout:      120 * time.Second,
		Sticky:                 true,
		StickyTTL:              time.Hour,
	}
}

// SetPolicy 替换调度与容错设置，立即生效。令牌缓存和号池各自持有的部分在同一把锁里下发，
// 并发修改时它们和快照里的策略一致。
func (r *Relay) SetPolicy(p Policy) {
	r.updateConfig(func(c *runtimeConfig) {
		c.policy = p
		r.tokens.SetPolicy(p.TokenRefresh)
		r.pool.SetDefaultConcurrency(p.Concurrency)
		r.pool.SetLowQuota(p.LowQuota)
		r.pool.SetRequireProxy(p.RequireProxy)
		r.pool.SetWeeklyQuota(p.WeeklyQuota)
	})
}

// ConcurrencyLimit 返回号池里某账号当前生效的并发上限；账号不在号池里（停用、不存在）时返回 0。
func (r *Relay) ConcurrencyLimit(accountID int64) int {
	for _, a := range r.pool.Accounts() {
		if a.ID == accountID {
			return r.pool.Limit(a.Account)
		}
	}
	return 0
}

// Policy 返回当前的调度与容错设置。
func (r *Relay) Policy() Policy {
	return r.config().policy
}
