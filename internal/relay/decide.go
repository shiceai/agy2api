package relay

import (
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
)

const (
	// capacityCooldown：模型全局容量不足，换号多半也没用，只短暂退避。
	capacityCooldown = 10 * time.Second
	// maxResetCooldown 是按上游恢复时间冷却的上限，防止上游给出离谱的时间。周额度最长约 7 天。
	maxResetCooldown = 8 * 24 * time.Hour
)

// 自动停用账号时记下的原因，管理端账号页显示。因 ReasonInvalidGrant 停用的账号重新导入（换上新凭据）后
// 会重新启用，见 server 的导入逻辑。
const (
	ReasonTOSViolation = "条款封禁（TOS_VIOLATION）"
	ReasonInvalidGrant = "refresh token 已失效（invalid_grant）"
)

// action 是对一次上游失败的处理方式。
type action int

const (
	actionRetry  action = iota // 换号重试
	actionReturn               // 不再换号，按分类返回给客户端
)

func (a action) String() string {
	if a == actionReturn {
		return "return"
	}
	return "retry"
}

// decision 是对一次上游非 2xx 响应的处理决定。
type decision struct {
	action action
	// cooldown > 0 时让该账号的这个模型冷却这么久。
	cooldown time.Duration
	// backoff 为真表示瞬时限流：冷却时长按该账号的连续次数算（见 rateLimitBackoff），cooldown 为 0。
	backoff bool
	// disable 非空时停用账号，值为停用原因。
	disable string
}

// decide 把上游错误的分类换成下一步动作。纯函数，不读写任何状态。
//
// 403 一律不在本次请求里换号：如果是请求内容触发的，换号可能连累其他账号；
// 账号状态已经更新，客户端重试时自然会换到别的号。401 先在 Generate 里刷新令牌同号重试，
// 仍是 401 才走到这里换号。
func decide(e antigravity.UpstreamError, p Policy, now time.Time) decision {
	switch e.Kind {
	case antigravity.KindTOSViolation:
		return decision{action: actionReturn, disable: ReasonTOSViolation}
	case antigravity.KindValidationRequired:
		return decision{action: actionReturn, cooldown: p.ValidationCooldown}
	case antigravity.KindModelForbidden:
		return decision{action: actionReturn, cooldown: p.ModelForbiddenCooldown}
	case antigravity.KindForbidden:
		return decision{action: actionReturn}
	case antigravity.KindQuotaExhausted, antigravity.KindRateLimit:
		if !e.ResetAt.IsZero() {
			return decision{action: actionRetry, cooldown: untilReset(e.ResetAt, now)}
		}
		return decision{action: actionRetry, backoff: true}
	case antigravity.KindModelCapacity:
		if !e.ResetAt.IsZero() {
			return decision{action: actionRetry, cooldown: untilReset(e.ResetAt, now)}
		}
		return decision{action: actionRetry, cooldown: capacityCooldown}
	case antigravity.KindServerError:
		return decision{action: actionRetry, cooldown: p.ServerErrorCooldown}
	case antigravity.KindUnauthorized:
		return decision{action: actionRetry}
	default: // 请求本身的问题，换号也一样
		return decision{action: actionReturn}
	}
}

// untilReset 返回到上游恢复时间的时长，限制在 [1 秒, maxResetCooldown]。
func untilReset(at, now time.Time) time.Duration {
	d := at.Sub(now)
	if d < time.Second {
		return time.Second
	}
	if d > maxResetCooldown {
		return maxResetCooldown
	}
	return d
}

// rateLimitBackoff 返回连续第 n 次瞬时限流的冷却：RateLimitBase × 2^(n-1)，不超过 RateLimitMax。
func rateLimitBackoff(n int, p Policy) time.Duration {
	d := p.RateLimitBase
	for i := 1; i < n && d < p.RateLimitMax; i++ {
		d *= 2
	}
	return min(d, p.RateLimitMax)
}
