package pool

import (
	"math"
	"sort"
	"time"
)

// 选号方式：会话粘性的优先账号接不了或没有时，从能用的账号（已排除冷却、并发已满、额度避让的）里怎么选。
const (
	StrategyRoundRobin = "round_robin" // 轮询：按顺序轮流
	StrategySmart      = "smart"       // 智能：按负载、剩余额度、近期失败率、首字延迟打分，分数接近的轮流
	StrategyResetFirst = "reset_first" // 重置优先：先用这个模型额度最快重置的账号
)

// 智能选号的系数和容差（参考 codex-proxy-rs 的默认值，固定不开放）：分数 = 负载×1 + 剩余额度×0.8 + 健康×1 + 延迟×0.5，
// 和最高分差在 smartTolerance 以内的账号轮流选，免得分数稍高的一个账号接下所有请求。
const (
	smartLoadWeight    = 1.0
	smartQuotaWeight   = 0.8
	smartHealthWeight  = 1.0
	smartLatencyWeight = 0.5
	smartTolerance     = 0.05
	// latencyHalfScore：首字延迟为它时延迟分是 0.5。
	latencyHalfScore = 10 * time.Second
	// unknownQuotaScore：没有额度信息的账号按剩一半算。
	unknownQuotaScore = 0.5
)

// 近期失败率的平滑：每次结果按 healthAlpha 计入，不再有结果时按 healthHalfLife 衰减，失败过的账号一段时间后恢复。
const (
	healthAlpha    = 0.2
	healthHalfLife = 15 * time.Minute
)

// resetTie：重置时间相差在这以内的按同时重置算，再比负载。
const resetTie = time.Minute

// SetStrategy 设置选号方式（Strategy*），不认识的按轮询。
func (p *Pool) SetStrategy(s string) {
	p.strategy.Store(s)
}

func (p *Pool) strategyName() string {
	s, _ := p.strategy.Load().(string)
	return s
}

// health 是账号近期的失败率（0～1，随时间衰减）和流式首字延迟（指数平滑）。只在内存里，重启后清零。
type health struct {
	failure   float64
	updated   time.Time
	latencyMs float64 // 0 表示还没有样本
}

// failureAt 返回 now 时衰减后的失败率。
func (h *health) failureAt(now time.Time) float64 {
	if h.updated.IsZero() || !now.After(h.updated) {
		return h.failure
	}
	return h.failure * math.Pow(0.5, now.Sub(h.updated).Seconds()/healthHalfLife.Seconds())
}

// note 计入一次结果：failed 为失败；firstByte 大于 0 时计入首字延迟。
func (h *health) note(failed bool, firstByte time.Duration, now time.Time) {
	sample := 0.0
	if failed {
		sample = 1
	}
	h.failure = healthAlpha*sample + (1-healthAlpha)*h.failureAt(now)
	h.updated = now
	if ms := float64(firstByte.Milliseconds()); ms > 0 {
		if h.latencyMs == 0 {
			h.latencyMs = ms
		} else {
			h.latencyMs = healthAlpha*ms + (1-healthAlpha)*h.latencyMs
		}
	}
}

// healthLocked 返回账号的统计，没有时新建。调用方持有 p.mu。
func (p *Pool) healthLocked(accountID int64) *health {
	h := p.health[accountID]
	if h == nil {
		h = &health{}
		p.health[accountID] = h
	}
	return h
}

// NoteFailure 记一次和账号有关的失败（连不上上游或代理、5xx、瞬时限流、流式第一帧前断开），计入近期失败率。
// 请求本身的问题、额度耗尽、全局容量不足不算：前者不是账号的问题，后两者有冷却和额度处理。
func (p *Pool) NoteFailure(accountID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthLocked(accountID).note(true, 0, time.Now())
}

// chooseLocked 按选号方式从 candidates（非空）里选一个。调用方持有 p.mu。
func (p *Pool) chooseLocked(candidates []candidate, now time.Time) candidate {
	switch p.strategyName() {
	case StrategySmart:
		return p.smartLocked(candidates, now)
	case StrategyResetFirst:
		return p.resetFirstLocked(candidates, now)
	}
	return p.rotate(candidates)
}

// rotate 按全局游标轮流选，每次推进游标。
func (p *Pool) rotate(candidates []candidate) candidate {
	return candidates[(p.cursor.Add(1)-1)%uint64(len(candidates))]
}

// load 返回账号占用的并发比例（0～1）。调用方持有 p.mu。
func (p *Pool) load(a *Account) float64 {
	limit := p.Limit(a.Account)
	if limit <= 0 {
		return 0
	}
	return min(float64(p.inflight[a.ID])/float64(limit), 1)
}

// smartScore 是智能选号的分数，越高越好。调用方持有 p.mu。
func (p *Pool) smartScore(c candidate, now time.Time) float64 {
	quota := unknownQuotaScore
	if rem, ok := p.quotaLeft(c.account, c.model, now); ok {
		quota = min(max(rem, 0), 1)
	}
	healthy, latency := 1.0, 1.0
	if h := p.health[c.account.ID]; h != nil {
		healthy = 1 - min(h.failureAt(now), 1)
		if h.latencyMs > 0 {
			half := float64(latencyHalfScore.Milliseconds())
			latency = half / (half + h.latencyMs)
		}
	}
	return smartLoadWeight*(1-p.load(c.account)) + smartQuotaWeight*quota + smartHealthWeight*healthy + smartLatencyWeight*latency
}

// smartLocked 选分数最高的，和最高分差在容差以内的按账号 ID 排好后轮流选。调用方持有 p.mu。
func (p *Pool) smartLocked(candidates []candidate, now time.Time) candidate {
	scores := make([]float64, len(candidates))
	best := math.Inf(-1)
	for i, c := range candidates {
		scores[i] = p.smartScore(c, now)
		best = max(best, scores[i])
	}
	top := make([]candidate, 0, len(candidates))
	for i, c := range candidates {
		if best-scores[i] <= smartTolerance {
			top = append(top, c)
		}
	}
	sort.Slice(top, func(i, j int) bool { return top[i].account.ID < top[j].account.ID })
	return p.rotate(top)
}

// resetAt 返回账号这个模型最早作废的额度的重置时间：5 小时额度和每周额度（开了时）里用过（剩余不到 100%）、
// 重置时间还没到的取较早的。都没用过或没有数据时为零值：这样的账号排在后面，先把已经开始的窗口用完。
// 调用方持有 p.mu。
func (p *Pool) resetAt(c candidate, now time.Time) time.Time {
	var at time.Time
	consider := func(t time.Time) {
		if t.After(now) && (at.IsZero() || t.Before(at)) {
			at = t
		}
	}
	if m, ok := c.account.models[c.model]; ok && m.RemainingFraction != nil && *m.RemainingFraction < 1 && m.ResetTime != "" {
		if t, err := time.Parse(time.RFC3339, m.ResetTime); err == nil {
			consider(t)
		}
	}
	if l, ok := p.weekly(c.account, c.model, now); ok && l.Remaining < 1 {
		consider(l.Reset)
	}
	return at
}

// resetFirstLocked 选额度最早重置的账号（相差 resetTie 以内的算同时），同时重置的选负载最低的，再相同的轮流。
// 都不知道重置时间时按负载选。调用方持有 p.mu。
func (p *Pool) resetFirstLocked(candidates []candidate, now time.Time) candidate {
	var earliest time.Time
	resets := make([]time.Time, len(candidates))
	for i, c := range candidates {
		resets[i] = p.resetAt(c, now)
		if !resets[i].IsZero() && (earliest.IsZero() || resets[i].Before(earliest)) {
			earliest = resets[i]
		}
	}
	group := make([]candidate, 0, len(candidates))
	for i, c := range candidates {
		if earliest.IsZero() || (!resets[i].IsZero() && resets[i].Sub(earliest) <= resetTie) {
			group = append(group, c)
		}
	}
	lowest := math.Inf(1)
	for _, c := range group {
		lowest = min(lowest, p.load(c.account))
	}
	least := group[:0:0]
	for _, c := range group {
		if p.load(c.account) == lowest {
			least = append(least, c)
		}
	}
	sort.Slice(least, func(i, j int) bool { return least[i].account.ID < least[j].account.ID })
	return p.rotate(least)
}
