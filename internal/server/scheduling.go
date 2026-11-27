package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/pool"
	"github.com/shiceai/agy2api/internal/relay"
)

// schedulingView 是调度与容错设置的读写形态，时长都以秒为单位，LowQuota 是百分比。
type schedulingView struct {
	TokenRefreshEarly      bool `json:"token_refresh_early"`
	TokenRefreshMinLead    int  `json:"token_refresh_min_lead"`
	TokenRefreshMaxLead    int  `json:"token_refresh_max_lead"`
	Concurrency            int  `json:"concurrency"`
	SlotWait               int  `json:"slot_wait"`
	MaxAttempts            int  `json:"max_attempts"`
	CooldownWait           int  `json:"cooldown_wait"`
	RateLimitBase          int  `json:"rate_limit_base"`
	RateLimitMax           int  `json:"rate_limit_max"`
	ServerErrorCooldown    int  `json:"server_error_cooldown"`
	ValidationCooldown     int  `json:"validation_cooldown"`
	ModelForbiddenCooldown int  `json:"model_forbidden_cooldown"`
	QuotaRefresh           int  `json:"quota_refresh"`
	LowQuota               int  `json:"low_quota"`
	StreamIdle             int  `json:"stream_idle"`
	RequireProxy           bool `json:"require_proxy"`
	// Strategy 是选号方式（pool.Strategy*）。
	Strategy string `json:"strategy"`
	// Sticky 是旧的会话粘性开关，保留给撤销在线升级后的旧版本读：StickyMode 不是关闭时为 true。
	// StickyMode 是会话粘性的方式（relay.Sticky*），旧库里没有时按 Sticky 换算，见 UnmarshalJSON。
	Sticky     bool   `json:"sticky"`
	StickyMode string `json:"sticky_mode"`
	StickyTTL  int    `json:"sticky_ttl"`
	// WeeklyQuota 是每周额度的全局开关（账号可单独开关），见 relay.Policy.WeeklyQuota。
	WeeklyQuota bool `json:"weekly_quota"`
}

// schedulingSetting 存在 settings 的 scheduling 键，默认值是 relay.DefaultPolicy。
var schedulingSetting = jsonSetting[schedulingView]{
	key:      "scheduling",
	name:     "调度设置",
	limit:    1 << 16,
	defaults: func() schedulingView { return schedulingFromPolicy(relay.DefaultPolicy()) },
	validate: schedulingView.validate,
}

func secs(d time.Duration) int { return int(d / time.Second) }

func dur(s int) time.Duration { return time.Duration(s) * time.Second }

func schedulingFromPolicy(p relay.Policy) schedulingView {
	return schedulingView{
		TokenRefreshEarly:      p.TokenRefresh.Early,
		TokenRefreshMinLead:    secs(p.TokenRefresh.MinLead),
		TokenRefreshMaxLead:    secs(p.TokenRefresh.MaxLead),
		Concurrency:            p.Concurrency,
		SlotWait:               secs(p.SlotWait),
		MaxAttempts:            p.MaxAttempts,
		CooldownWait:           secs(p.CooldownWait),
		RateLimitBase:          secs(p.RateLimitBase),
		RateLimitMax:           secs(p.RateLimitMax),
		ServerErrorCooldown:    secs(p.ServerErrorCooldown),
		ValidationCooldown:     secs(p.ValidationCooldown),
		ModelForbiddenCooldown: secs(p.ModelForbiddenCooldown),
		QuotaRefresh:           secs(p.QuotaRefresh),
		LowQuota:               int(math.Round(p.LowQuota * 100)),
		StreamIdle:             secs(p.StreamIdleTimeout),
		RequireProxy:           p.RequireProxy,
		Strategy:               p.Strategy,
		Sticky:                 p.Sticky != relay.StickyOff,
		StickyMode:             p.Sticky,
		StickyTTL:              secs(p.StickyTTL),
		WeeklyQuota:            p.WeeklyQuota,
	}
}

func (v schedulingView) policy() relay.Policy {
	return relay.Policy{
		TokenRefresh: antigravity.RefreshPolicy{
			Early:   v.TokenRefreshEarly,
			MinLead: dur(v.TokenRefreshMinLead),
			MaxLead: dur(v.TokenRefreshMaxLead),
		},
		Concurrency:   v.Concurrency,
		SlotWait:      dur(v.SlotWait),
		MaxAttempts:   v.MaxAttempts,
		CooldownWait:  dur(v.CooldownWait),
		RateLimitBase: dur(v.RateLimitBase),
		RateLimitMax:  dur(v.RateLimitMax),
		// 亚秒级的瞬时 429 重试间隔不在秒制设置项里暴露，用默认值。
		RateLimitRetryDelay:    relay.DefaultPolicy().RateLimitRetryDelay,
		ServerErrorCooldown:    dur(v.ServerErrorCooldown),
		ValidationCooldown:     dur(v.ValidationCooldown),
		ModelForbiddenCooldown: dur(v.ModelForbiddenCooldown),
		QuotaRefresh:           dur(v.QuotaRefresh),
		LowQuota:               float64(v.LowQuota) / 100,
		StreamIdleTimeout:      dur(v.StreamIdle),
		RequireProxy:           v.RequireProxy,
		Strategy:               v.Strategy,
		Sticky:                 v.StickyMode,
		StickyTTL:              dur(v.StickyTTL),
		WeeklyQuota:            v.WeeklyQuota,
	}
}

// UnmarshalJSON 在已有值上解码，并让 Sticky 和 StickyMode 一致：给了 sticky_mode 时按它设 Sticky；
// 只有旧的 sticky 时（升级前存的设置）按它换算，开着的是严格（新的默认），关着的是关闭。
func (v *schedulingView) UnmarshalJSON(b []byte) error {
	type plain schedulingView
	var given struct {
		Sticky     *bool   `json:"sticky"`
		StickyMode *string `json:"sticky_mode"`
	}
	if err := json.Unmarshal(b, (*plain)(v)); err != nil {
		return err
	}
	if err := json.Unmarshal(b, &given); err != nil {
		return err
	}
	switch {
	case given.StickyMode != nil:
		v.Sticky = v.StickyMode != relay.StickyOff
	case given.Sticky != nil && *given.Sticky:
		v.StickyMode = relay.StickyStrict
	case given.Sticky != nil:
		v.StickyMode = relay.StickyOff
	}
	return nil
}

// validate 检查取值范围。access token 有效期 1 小时，提前量最多 50 分钟。额度刷新间隔 0 表示关闭，
// 开启时 5 分钟到 2 小时：太频繁会给上游添无谓的请求。流式空闲超时 0 表示不断开，开启时 30 秒到 10 分钟：
// 太短会误断正常的长思考。会话粘性的闲置失效 5 分钟到 24 小时。
func (v schedulingView) validate() error {
	checks := []struct {
		name     string
		val      int
		min, max int
	}{
		{"token_refresh_min_lead", v.TokenRefreshMinLead, 60, 1800},
		{"token_refresh_max_lead", v.TokenRefreshMaxLead, v.TokenRefreshMinLead, 3000},
		{"concurrency", v.Concurrency, 1, 64},
		{"slot_wait", v.SlotWait, 0, 120},
		{"max_attempts", v.MaxAttempts, 1, 10},
		{"cooldown_wait", v.CooldownWait, 0, 120},
		{"rate_limit_base", v.RateLimitBase, 1, 3600},
		{"rate_limit_max", v.RateLimitMax, v.RateLimitBase, 86400},
		{"server_error_cooldown", v.ServerErrorCooldown, 0, 3600},
		{"validation_cooldown", v.ValidationCooldown, 0, 86400},
		{"model_forbidden_cooldown", v.ModelForbiddenCooldown, 0, 86400},
		{"low_quota", v.LowQuota, 0, 50},
		{"sticky_ttl", v.StickyTTL, 300, 86400},
	}
	for _, c := range checks {
		if c.val < c.min || c.val > c.max {
			return fmt.Errorf("%s must be between %d and %d", c.name, c.min, c.max)
		}
	}
	switch v.Strategy {
	case pool.StrategyRoundRobin, pool.StrategySmart, pool.StrategyResetFirst:
	default:
		return fmt.Errorf("strategy must be round_robin, smart or reset_first")
	}
	switch v.StickyMode {
	case relay.StickyOff, relay.StickyPreferred, relay.StickyStrict:
	default:
		return fmt.Errorf("sticky_mode must be off, preferred or strict")
	}
	if v.QuotaRefresh != 0 && (v.QuotaRefresh < 300 || v.QuotaRefresh > 7200) {
		return fmt.Errorf("quota_refresh must be 0 (off) or between 300 and 7200")
	}
	if v.StreamIdle != 0 && (v.StreamIdle < 30 || v.StreamIdle > 600) {
		return fmt.Errorf("stream_idle must be 0 (off) or between 30 and 600")
	}
	return nil
}

// loadScheduling 启动时从库加载调度设置并生效。
func (s *Service) loadScheduling(ctx context.Context) error {
	v, err := schedulingSetting.load(ctx, s.store)
	if err != nil {
		return err
	}
	s.relay.SetPolicy(v.policy())
	return nil
}

func (s *Service) adminGetScheduling(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": schedulingFromPolicy(s.relay.Policy()),
		"defaults": schedulingSetting.defaults(),
	})
}

func (s *Service) adminUpdateScheduling(w http.ResponseWriter, r *http.Request) {
	// 缺省的字段保持当前值。
	v, ok := schedulingSetting.update(w, r, s.store, schedulingFromPolicy(s.relay.Policy()))
	if !ok {
		return
	}
	s.relay.SetPolicy(v.policy())
	slog.InfoContext(r.Context(), "已修改调度设置", "settings", v)
	s.adminGetScheduling(w, r)
}
