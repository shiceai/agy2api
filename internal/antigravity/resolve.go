package antigravity

import (
	"fmt"
	"slices"
	"strings"
)

// tierOrder 是分档模型的档位后缀，按思考强度从低到高。上游目录里的分档模型 ID 形如
// <家族>-<档位>，如 gemini-3.8-flash-high、claude-opus-5-5-low。
var tierOrder = []string{"low", "medium", "high", "xhigh"}

// DefaultTier 是管理端没有配置时，家族名请求不带思考参数所用的档位。
const DefaultTier = "medium"

// Tiers 返回所有档位，按思考强度从低到高。
func Tiers() []string {
	return slices.Clone(tierOrder)
}

// ParseTier 把档位名（不区分大小写）规范成小写档位，MINIMAL 视作最低档。不认识时返回 false。
func ParseTier(s string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "minimal" {
		return tierOrder[0], true
	}
	if slices.Contains(tierOrder, t) {
		return t, true
	}
	return "", false
}

// RequestedTier 从 Gemini 请求体读出客户端要求的档位：thinkingLevel 优先，
// 否则按 thinkingBudget 换算；没有思考参数时返回空串。camelCase 和 snake_case 字段名都认。
func RequestedTier(body map[string]any) string {
	gen, _ := field(body, "generationConfig", "generation_config").(map[string]any)
	tc, _ := field(gen, "thinkingConfig", "thinking_config").(map[string]any)
	if tc == nil {
		return ""
	}
	if level, ok := field(tc, "thinkingLevel", "thinking_level").(string); ok {
		if t, ok := ParseTier(level); ok {
			return t
		}
	}
	if budget, ok := field(tc, "thinkingBudget", "thinking_budget").(float64); ok {
		return tierForBudget(budget)
	}
	return ""
}

// tierForBudget 把 Gemini 2.5 风格的思考预算换算成档位：-1（动态）按 high，
// 0 到 1024 按 low，到 8192 按 medium，再大按 high。
func tierForBudget(budget float64) string {
	switch {
	case budget < 0:
		return "high"
	case budget <= 1024:
		return "low"
	case budget <= 8192:
		return "medium"
	default:
		return "high"
	}
}

// ResolveModel 在目录里为请求的模型找精确 ID，has 判断目录里有没有某个 ID：
//   - 目录里有同名模型，原样返回；
//   - 请求的是家族名（目录里有 <家族>-<档位>），按 tier 选档；家族没有该档时先往低、再往高找最接近的档；
//   - 都找不到返回 false。
//
// tier 不认识时按 DefaultTier 处理。
func ResolveModel(has func(id string) bool, requested, tier string) (string, bool) {
	if has(requested) {
		return requested, true
	}
	idx := slices.Index(tierOrder, tier)
	if idx < 0 {
		idx = slices.Index(tierOrder, DefaultTier)
	}
	for i := idx; i >= 0; i-- {
		if id := requested + "-" + tierOrder[i]; has(id) {
			return id, true
		}
	}
	for i := idx + 1; i < len(tierOrder); i++ {
		if id := requested + "-" + tierOrder[i]; has(id) {
			return id, true
		}
	}
	return "", false
}

// TierFamilies 从模型 ID 里找出分档家族及其可用档位（按思考强度从低到高），键是家族名。
// 家族名就是去掉档位后缀的部分，与 ResolveModel 的家族解析一致。
func TierFamilies(ids []string) map[string][]string {
	out := map[string][]string{}
	for _, id := range ids {
		for _, t := range tierOrder {
			if family, ok := strings.CutSuffix(id, "-"+t); ok && family != "" && !slices.Contains(out[family], t) {
				out[family] = append(out[family], t)
			}
		}
	}
	for _, tiers := range out {
		slices.SortFunc(tiers, func(a, b string) int {
			return slices.Index(tierOrder, a) - slices.Index(tierOrder, b)
		})
	}
	return out
}

// IsTierModel 判断模型 ID 是否带档位后缀。分档模型的思考强度由模型名决定。
func IsTierModel(id string) bool {
	return TierOf(id) != ""
}

// TierOf 返回模型 ID 的档位后缀，不是分档模型时返回空串。
func TierOf(id string) string {
	for _, t := range tierOrder {
		if strings.HasSuffix(id, "-"+t) {
			return t
		}
	}
	return ""
}

// ReasoningLabel 描述一次请求实际的推理强度，供用量记录展示：分档模型取模型名里的档位；
// 其它模型取客户端的 thinkingLevel（小写），或把 thinkingBudget 记成 "budget:<N>"；都没有时为空串。
func ReasoningLabel(wireModel string, body map[string]any) string {
	if t := TierOf(wireModel); t != "" {
		return t
	}
	gen, _ := field(body, "generationConfig", "generation_config").(map[string]any)
	tc, _ := field(gen, "thinkingConfig", "thinking_config").(map[string]any)
	if level, ok := field(tc, "thinkingLevel", "thinking_level").(string); ok && strings.TrimSpace(level) != "" {
		return strings.ToLower(strings.TrimSpace(level))
	}
	if budget, ok := field(tc, "thinkingBudget", "thinking_budget").(float64); ok {
		return fmt.Sprintf("budget:%d", int64(budget))
	}
	return ""
}

// field 按顺序取 m 里第一个存在的字段，m 为 nil 或都不存在时返回 nil。
func field(m map[string]any, names ...string) any {
	for _, n := range names {
		if v, ok := m[n]; ok {
			return v
		}
	}
	return nil
}
