package antigravity

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// ModelInfo 是模型目录里一个模型的精简视图。
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	// Enum 是上游下发的模型枚举名（如 MODEL_PLACEHOLDER_M318），labels.model_enum 要填它而不是 ID。
	Enum              string   `json:"enum,omitempty"`
	SupportsThinking  bool     `json:"supports_thinking"`
	ThinkingBudget    *int     `json:"thinking_budget,omitempty"`
	MaxOutputTokens   *int     `json:"max_output_tokens,omitempty"`
	SupportsImages    bool     `json:"supports_images"`
	RemainingFraction *float64 `json:"remaining_fraction,omitempty"`
	ResetTime         string   `json:"reset_time,omitempty"`
	// Listed 为真时出现在对外的模型列表里：官方客户端的模型选择器（agentModelSorts）或辅助用途（命令、网页搜索、
	// 提交信息、生图等）里有它。见 PublicModels。
	Listed bool `json:"listed,omitempty"`
	// Family 是官方分组（groupedModels）里的家族名，如 gemini-4-neon-high 的 gemini-4-neon；对外列表用家族名
	// 代替各档位。FamilyDisplayName 是家族的显示名（如 Gemini 4 Neon）。
	Family            string `json:"family,omitempty"`
	FamilyDisplayName string `json:"family_display_name,omitempty"`
}

type fetchModelsResponse struct {
	Models map[string]struct {
		DisplayName      string `json:"displayName"`
		Model            string `json:"model"`
		IsInternal       bool   `json:"isInternal"`
		SupportsImages   *bool  `json:"supportsImages"`
		SupportsThinking *bool  `json:"supportsThinking"`
		ThinkingBudget   *int   `json:"thinkingBudget"`
		MaxOutputTokens  *int   `json:"maxOutputTokens"`
		QuotaInfo        *struct {
			RemainingFraction *float64 `json:"remainingFraction"`
			ResetTime         string   `json:"resetTime"`
		} `json:"quotaInfo"`
	} `json:"models"`
	// 以下是官方客户端挑模型用的信息（2026-11-26 实抓）：模型选择器里的模型、按档位分组的家族、辅助用途的模型。
	AgentModelSorts []struct {
		Groups []struct {
			ModelIDs []string `json:"modelIds"`
		} `json:"groups"`
	} `json:"agentModelSorts"`
	GroupedModels map[string]struct {
		DisplayName             string `json:"displayName"`
		SupportedThinkingLevels []struct {
			ThinkingSuffix string `json:"thinkingSuffix"`
		} `json:"supportedThinkingLevels"`
	} `json:"groupedModels"`
	CommandModelIDs         []string `json:"commandModelIds"`
	ImageGenerationModelIDs []string `json:"imageGenerationModelIds"`
	MqueryModelIDs          []string `json:"mqueryModelIds"`
	WebSearchModelIDs       []string `json:"webSearchModelIds"`
	CommitMessageModelIDs   []string `json:"commitMessageModelIds"`
}

// markListed 按官方客户端挑模型用的信息标出对外列出的模型和它们所属的家族（见 ModelInfo.Listed、Family）。
func (r *fetchModelsResponse) markListed(models []ModelInfo) {
	listed := map[string]bool{}
	for _, picker := range r.AgentModelSorts {
		for _, g := range picker.Groups {
			for _, id := range g.ModelIDs {
				listed[id] = true
			}
		}
	}
	for _, ids := range [][]string{r.CommandModelIDs, r.ImageGenerationModelIDs, r.MqueryModelIDs, r.WebSearchModelIDs, r.CommitMessageModelIDs} {
		for _, id := range ids {
			listed[id] = true
		}
	}
	type family struct{ name, display string }
	families := map[string]family{}
	for name, g := range r.GroupedModels {
		for _, level := range g.SupportedThinkingLevels {
			if level.ThinkingSuffix != "" {
				families[name+"-"+level.ThinkingSuffix] = family{name, g.DisplayName}
			}
		}
	}
	for i := range models {
		models[i].Listed = listed[models[i].ID]
		if f, ok := families[models[i].ID]; ok {
			models[i].Family, models[i].FamilyDisplayName = f.name, f.display
		}
	}
}

// FetchAvailableModels 取账号的模型目录与额度快照。
// 返回精简后的模型列表，供目录展示与选号；原始 JSON 由调用方按需保留。
func (c *Client) FetchAvailableModels(ctx context.Context, cred *Credential) ([]ModelInfo, error) {
	req := map[string]any{}
	if p := strings.TrimSpace(cred.ProjectID); p != "" {
		req["project"] = p
	}
	var resp fetchModelsResponse
	if _, err := c.postV1Internal(ctx, cred, "fetchAvailableModels", req, &resp, nil); err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(resp.Models))
	for id, m := range resp.Models {
		id = strings.TrimSpace(id)
		if id == "" || m.IsInternal {
			continue
		}
		info := ModelInfo{
			ID:              id,
			DisplayName:     m.DisplayName,
			Enum:            strings.TrimSpace(m.Model),
			ThinkingBudget:  m.ThinkingBudget,
			MaxOutputTokens: m.MaxOutputTokens,
		}
		if m.SupportsThinking != nil {
			info.SupportsThinking = *m.SupportsThinking
		}
		if m.SupportsImages != nil {
			info.SupportsImages = *m.SupportsImages
		}
		if m.QuotaInfo != nil {
			// 额度用完时上游只给 resetTime：proto3 的 JSON 省略了值为 0 的 remainingFraction。
			info.RemainingFraction = m.QuotaInfo.RemainingFraction
			if info.RemainingFraction == nil {
				zero := 0.0
				info.RemainingFraction = &zero
			}
			info.ResetTime = strings.TrimSpace(m.QuotaInfo.ResetTime)
		}
		out = append(out, info)
	}
	resp.markListed(out)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ListedModel 是对外模型列表里的一项：官方分组的家族用家族名（Tiers 是它的各档位），其余是模型本身。
type ListedModel struct {
	ID          string
	DisplayName string
	Tiers       []ModelInfo
	// Info 是这一项在目录里的条目（家族取其中一档），给模型记录用。
	Info ModelInfo
}

// PublicModels 从号池目录（各账号目录的并集）挑出对外列出的模型：官方客户端的模型选择器和辅助用途里有的模型，
// 分档的合成家族名（档位由请求的思考参数或默认档位决定，带档位的名字照样能调用）。目录还没带这些信息时
// （升级前存下、还没刷新的目录）列出全部，和以前一样。按 ID 排序。
func PublicModels(catalog []ModelInfo) []ListedModel {
	flagged := false
	for _, m := range catalog {
		flagged = flagged || m.Listed
	}
	var out []ListedModel
	families := map[string]int{} // 家族名 → out 里的下标
	for _, m := range catalog {
		switch {
		case !flagged:
			out = append(out, ListedModel{ID: m.ID, DisplayName: m.DisplayName, Info: m})
		case !m.Listed:
		case m.Family != "":
			if i, ok := families[m.Family]; ok {
				out[i].Tiers = append(out[i].Tiers, m)
				continue
			}
			families[m.Family] = len(out)
			name := m.FamilyDisplayName
			if name == "" {
				name = m.Family
			}
			out = append(out, ListedModel{ID: m.Family, DisplayName: name, Tiers: []ModelInfo{m}, Info: m})
		default:
			out = append(out, ListedModel{ID: m.ID, DisplayName: m.DisplayName, Info: m})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// IsUnavailablePlaceholder 报告模型 ID 是不是上游的占位条目：某个模型对这个账号不开放时，目录里不给它的各档，
// 只放一个 <家族>-disabled（如 gemini-4-carbon-disabled，显示名仍是 Gemini 4 Carbon，2026-11-23 的目录里见过）。
// 它不是能调用的模型：不参与解析、选号和对外列表，管理端显示成"不能用"。
func IsUnavailablePlaceholder(id string) bool {
	for _, seg := range strings.FieldsFunc(strings.ToLower(id), func(r rune) bool { return r == '-' || r == '_' }) {
		if seg == "disabled" {
			return true
		}
	}
	return false
}

// MarshalModels 把模型列表序列化为存库用的 JSON 文本。
func MarshalModels(models []ModelInfo) string {
	if len(models) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(models)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// UnmarshalModels 还原存库的模型列表 JSON。
func UnmarshalModels(raw string) []ModelInfo {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []ModelInfo
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
