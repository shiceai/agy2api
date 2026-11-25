// Package pricing 按官方 API 价格估算用量的费用（只显示，不扣费）：从厂商官方价格页同步单价，
// 管理端可手动设价覆盖；用量记录落库前按记录时的价格算好费用，没有价格的记录之后有了价格再补算。
package pricing

import "time"

// Rates 是一组单价：美元每百万 token，按百万分之一美元存整数（$0.075 存 75000）。
type Rates struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	// CacheRead 是命中缓存的输入 token 的单价；nil 表示没有单独的价格，按输入价算。
	CacheRead *int64 `json:"cache_read,omitempty"`
}

// Tier 是按提示长度的分档：输入 token 总数（含命中缓存的部分）超过 Threshold 时，整次请求按这一档计价。
type Tier struct {
	Threshold int64 `json:"threshold"`
	Rates
}

// Promo 是限时价：End（Unix 毫秒）之前按这组单价计价，之后按标准价。各项都已填好，页面上没写限时价的项沿用标准价。
type Promo struct {
	End int64 `json:"end"`
	Rates
}

// Price 是一个模型的价格：标准单价，可选按提示长度分一档、限时价（两者不同时出现）。
type Price struct {
	Rates
	Tier  *Tier  `json:"tier,omitempty"`
	Promo *Promo `json:"promo,omitempty"`
}

// Usage 是一次请求计费用的 token 数，与用量记录一致。
type Usage struct {
	Prompt   int64 // 输入总数，含命中缓存的部分
	Cached   int64 // 命中缓存的输入
	Output   int64 // 回复，不含思考
	Thoughts int64 // 思考，按输出价计（官方价格页写明输出价包含思考 token）
}

// Applied 是一次请求实际用的单价，以及是否用了分档价、限时价。
type Applied struct {
	Rates
	Tier  bool
	Promo bool
}

// At 返回在 at 时刻、提示长度为 prompt 的请求适用的单价。
func (p Price) At(prompt int64, at time.Time) Applied {
	switch {
	case p.Promo != nil && at.UnixMilli() < p.Promo.End:
		return Applied{Rates: p.Promo.Rates, Promo: true}
	case p.Tier != nil && prompt > p.Tier.Threshold:
		return Applied{Rates: p.Tier.Rates, Tier: true}
	}
	return Applied{Rates: p.Rates}
}

// Nanos 按单价算费用，单位十亿分之一美元，四舍五入。
// token 数 × 百万分之一美元每百万 token = 10⁻¹² 美元，除以 1000 得十亿分之一美元。
func (r Rates) Nanos(u Usage) int64 {
	cached := min(max(u.Cached, 0), u.Prompt)
	cacheRate := r.Input
	if r.CacheRead != nil {
		cacheRate = *r.CacheRead
	}
	sum := (u.Prompt-cached)*r.Input + cached*cacheRate + (u.Output+u.Thoughts)*r.Output
	return (sum + 500) / 1000
}

// valid 报告单价是否可用：输入、输出价不为负且不全为 0，缓存价不为负。
func (r Rates) valid() bool {
	return r.Input >= 0 && r.Output >= 0 && r.Input+r.Output > 0 && (r.CacheRead == nil || *r.CacheRead >= 0)
}

// Valid 报告价格是否可用（手动设价时校验）。
func (p Price) Valid() bool {
	if !p.valid() {
		return false
	}
	if p.Tier != nil && (p.Tier.Threshold <= 0 || !p.Tier.valid()) {
		return false
	}
	if p.Promo != nil && (p.Promo.End <= 0 || !p.Promo.valid()) {
		return false
	}
	return p.Tier == nil || p.Promo == nil
}
