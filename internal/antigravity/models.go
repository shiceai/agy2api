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
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
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
