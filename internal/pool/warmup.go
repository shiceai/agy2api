package pool

import (
	"math"
	"time"
)

// 新号预热与请求节奏：参考 codex-proxy-rs 的"带开关的预热"和 gcl2api-plus 的动态放量。
// 刚导入的账号不立刻承接全部流量，而是从低并发、较大请求间隔起步，随号龄和累计成功数逐步放量到正常水平，
// 让上游看到的是渐进的使用曲线而不是突然打满。整套由开关控制，默认关。

// WarmupConfig 是预热与节奏的设置。Enabled 为假时整套不生效。
type WarmupConfig struct {
	Enabled bool
	// InitialConcurrency 是预热初期的并发上限（通常 1），随进度升到账号正常上限。
	InitialConcurrency int
	// Successes、Age 是预热进度的两个维度：累计成功数和号龄各算一份，取较大者（任一达标就算预热完成）。
	// 用较大者而不是较小者：重启丢失成功计数后，老账号靠号龄仍是满进度，不会被重新限流。<=0 表示该维度不参与。
	Successes int
	Age       time.Duration
	// InitialInterval、MinInterval 是同一账号两次请求的最小间隔：从预热初期的 InitialInterval 随进度降到稳态的
	// MinInterval。MinInterval 为 0 表示预热完成后不再限制间隔。
	InitialInterval time.Duration
	MinInterval     time.Duration
}

// SetWarmup 设置预热与节奏，立即生效。
func (p *Pool) SetWarmup(c WarmupConfig) {
	cfg := c
	p.warmup.Store(&cfg)
}

// warmupCfg 返回生效中的预热设置；未设置或未启用时返回 nil。
func (p *Pool) warmupCfg() *WarmupConfig {
	c, _ := p.warmup.Load().(*WarmupConfig)
	if c == nil || !c.Enabled {
		return nil
	}
	return c
}

// WarmupState 是一个账号此刻的预热情况，账号页据此显示预热进度和压低后的并发上限。
type WarmupState struct {
	Progress      float64       // 预热进度 0～1，号龄和成功数各算一份取较大者
	Successes     int           // 已攒的成功数（内存计数，重启清零）
	SuccessTarget int           // 成功数达标值，0 表示不按成功数
	Age           time.Duration // 号龄
	AgeTarget     time.Duration // 号龄达标值，0 表示不按号龄
	Limit         int           // 预热后的并发上限
	Normal        int           // 正常并发上限
	Interval      time.Duration // 当前同一账号两次请求的最小间隔
}

// WarmupStatus 返回号池里还在预热的账号（账号 ID → 预热情况）；预热关闭时返回 nil，已预热完的账号不在表里。
// 只读内存（账号快照和成功计数），不读库，供账号页轮询。
func (p *Pool) WarmupStatus(now time.Time) map[int64]WarmupState {
	wc := p.warmupCfg()
	if wc == nil {
		return nil
	}
	accounts := p.snapshot()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[int64]WarmupState{}
	for _, a := range accounts {
		prog := p.warmProgressLocked(wc, a, now)
		if prog >= 1 {
			continue
		}
		normal := p.Limit(a.Account)
		var age time.Duration
		if a.CreatedAt > 0 {
			age = now.Sub(time.UnixMilli(a.CreatedAt))
		}
		out[a.ID] = WarmupState{
			Progress:      prog,
			Successes:     p.success[a.ID],
			SuccessTarget: max(wc.Successes, 0),
			Age:           age,
			AgeTarget:     max(wc.Age, 0),
			Limit:         warmLimit(wc, normal, prog),
			Normal:        normal,
			Interval:      warmInterval(wc, prog),
		}
	}
	return out
}

// warmProgressLocked 返回账号的预热进度（0～1，1 表示预热完成）。两个维度都关时按完成算。需持 p.mu。
func (p *Pool) warmProgressLocked(c *WarmupConfig, a *Account, now time.Time) float64 {
	if c.Age <= 0 && c.Successes <= 0 {
		return 1
	}
	prog := 0.0
	if c.Age > 0 && a.CreatedAt > 0 {
		prog = clamp01(float64(now.Sub(time.UnixMilli(a.CreatedAt))) / float64(c.Age))
	}
	if c.Successes > 0 {
		prog = math.Max(prog, clamp01(float64(p.success[a.ID])/float64(c.Successes)))
	}
	return prog
}

// warmLimit 按进度把并发上限从 InitialConcurrency 线性抬到 normal。
func warmLimit(c *WarmupConfig, normal int, progress float64) int {
	initial := c.InitialConcurrency
	if initial < 1 {
		initial = 1
	}
	if initial >= normal {
		return normal
	}
	lim := initial + int(math.Round(float64(normal-initial)*progress))
	if lim < initial {
		return initial
	}
	if lim > normal {
		return normal
	}
	return lim
}

// warmInterval 按进度把最小请求间隔从 InitialInterval 线性降到 MinInterval。
func warmInterval(c *WarmupConfig, progress float64) time.Duration {
	d := float64(c.InitialInterval) + (float64(c.MinInterval)-float64(c.InitialInterval))*progress
	if d < 0 {
		return 0
	}
	return time.Duration(d)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
