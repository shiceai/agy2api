package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/shiceai/agy2api/internal/pool"
)

// warmupView 是新号预热与请求节奏设置的读写形态。间隔用毫秒、号龄用分钟，和号池里的 time.Duration 换算。
type warmupView struct {
	Enabled bool `json:"enabled"`
	// InitialConcurrency 是预热初期的并发上限。
	InitialConcurrency int `json:"initial_concurrency"`
	// Successes、AgeMinutes 是预热进度的两个维度，任一达标即预热完成；0 表示该维度不参与。
	Successes  int `json:"successes"`
	AgeMinutes int `json:"age_minutes"`
	// InitialIntervalMs、MinIntervalMs 是同一账号两次请求的最小间隔：从预热初期降到稳态。
	InitialIntervalMs int `json:"initial_interval_ms"`
	MinIntervalMs     int `json:"min_interval_ms"`
}

// warmupSetting 存在 settings 的 warmup 键。默认关闭；开启后新号从并发 1、间隔 1.5 秒起步，攒够 20 次成功或满 2 小时放开。
var warmupSetting = jsonSetting[warmupView]{
	key:   "warmup",
	name:  "预热与节奏设置",
	limit: 1 << 12,
	defaults: func() warmupView {
		return warmupView{Enabled: false, InitialConcurrency: 1, Successes: 20, AgeMinutes: 120, InitialIntervalMs: 1500, MinIntervalMs: 0}
	},
	validate: warmupView.validate,
}

func (v warmupView) validate() error {
	if v.InitialConcurrency < 1 || v.InitialConcurrency > 1000 {
		return errors.New("initial_concurrency 应在 1～1000")
	}
	if v.Successes < 0 || v.Successes > 100000 {
		return errors.New("successes 应在 0～100000")
	}
	if v.AgeMinutes < 0 || v.AgeMinutes > 10080 {
		return errors.New("age_minutes 应在 0～10080")
	}
	if v.InitialIntervalMs < 0 || v.InitialIntervalMs > 600000 || v.MinIntervalMs < 0 || v.MinIntervalMs > 600000 {
		return errors.New("间隔应在 0～600000ms")
	}
	if v.MinIntervalMs > v.InitialIntervalMs {
		return errors.New("min_interval_ms 不得大于 initial_interval_ms")
	}
	return nil
}

func (v warmupView) config() pool.WarmupConfig {
	return pool.WarmupConfig{
		Enabled:            v.Enabled,
		InitialConcurrency: v.InitialConcurrency,
		Successes:          v.Successes,
		Age:                time.Duration(v.AgeMinutes) * time.Minute,
		InitialInterval:    time.Duration(v.InitialIntervalMs) * time.Millisecond,
		MinInterval:        time.Duration(v.MinIntervalMs) * time.Millisecond,
	}
}

func (v warmupView) apply(s *Service) {
	s.relay.SetWarmup(v.config())
}

// warmupStateView 是还在预热的账号此刻的情况：账号页据此显示"预热 N%"和压低后的并发上限。
// 随账号视图带一份快照，/live 接口实时刷新。
type warmupStateView struct {
	Progress      float64 `json:"progress"`       // 预热进度 0～1，号龄和成功数各算一份取较大者
	Successes     int     `json:"successes"`      // 已攒的成功数（内存计数，重启清零）
	SuccessTarget int     `json:"success_target"` // 成功数达标值，0 表示不按成功数
	AgeMinutes    int     `json:"age_minutes"`    // 号龄（分钟）
	AgeTarget     int     `json:"age_target"`     // 号龄达标值（分钟），0 表示不按号龄
	Limit         int     `json:"limit"`          // 预热后的并发上限
	Normal        int     `json:"normal"`         // 正常并发上限
	IntervalMs    int64   `json:"interval_ms"`    // 当前同一账号两次请求的最小间隔
}

func toWarmupStateView(w pool.WarmupState) *warmupStateView {
	return &warmupStateView{
		Progress:      w.Progress,
		Successes:     w.Successes,
		SuccessTarget: w.SuccessTarget,
		AgeMinutes:    int(w.Age / time.Minute),
		AgeTarget:     int(w.AgeTarget / time.Minute),
		Limit:         w.Limit,
		Normal:        w.Normal,
		IntervalMs:    w.Interval.Milliseconds(),
	}
}

// warmupStateViews 把号池的预热情况转成接口形态（账号 ID → 情况），预热关闭时为空表。
func warmupStateViews(states map[int64]pool.WarmupState) map[int64]*warmupStateView {
	out := make(map[int64]*warmupStateView, len(states))
	for id, w := range states {
		out[id] = toWarmupStateView(w)
	}
	return out
}

// loadWarmup 启动时从库加载预热与节奏设置并生效。
func (s *Service) loadWarmup(ctx context.Context) error {
	v, err := warmupSetting.load(ctx, s.store)
	if err != nil {
		return err
	}
	v.apply(s)
	return nil
}

func (s *Service) adminGetWarmup(w http.ResponseWriter, r *http.Request) {
	v, err := warmupSetting.load(r.Context(), s.store)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": v})
}

func (s *Service) adminUpdateWarmup(w http.ResponseWriter, r *http.Request) {
	v, ok := warmupSetting.update(w, r, s.store, warmupSetting.defaults())
	if !ok {
		return
	}
	v.apply(s)
	slog.InfoContext(r.Context(), "已修改预热与节奏设置", "enabled", v.Enabled, "initial_concurrency", v.InitialConcurrency)
	s.adminGetWarmup(w, r)
}
