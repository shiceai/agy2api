package antigravity

import (
	"context"
	"encoding/json"
	"strings"
)

// 每周额度：retrieveUserQuotaSummary 返回按组分的额度，每组有 5 小时和每周等窗口。fetchAvailableModels 的单模型
// 额度只反映 5 小时窗口，每周额度只能从这里拿。参考项目 gcl2api-plus、codex2api 都调它，官方客户端本地服务也有
// 这个接口，但手头的官方客户端抓包里没见过它发出这个请求，所以默认不调，由管理端按账号开启（见 relay 的后台刷新）。

// QuotaSummary 是账号的分组额度。
type QuotaSummary struct {
	Groups []QuotaGroup `json:"groups"`
}

// QuotaGroup 是一组共享额度的模型（如 Gemini、Claude 和 GPT），Description 是上游的说明（含组里的模型）。
type QuotaGroup struct {
	DisplayName string        `json:"display_name,omitempty"`
	Description string        `json:"description,omitempty"`
	Buckets     []QuotaBucket `json:"buckets"`
}

// QuotaBucket 是组里的一个额度窗口。Window 是上游的窗口名（如 5h、weekly）。RemainingFraction 为 nil 时上游没给
// 剩余比例（proto3 的 JSON 会省略值为 0 的字段，有重置时间而没有比例时多半是用完了）。
type QuotaBucket struct {
	BucketID          string   `json:"bucket_id,omitempty"`
	Window            string   `json:"window,omitempty"`
	DisplayName       string   `json:"display_name,omitempty"`
	Description       string   `json:"description,omitempty"`
	RemainingFraction *float64 `json:"remaining_fraction,omitempty"`
	ResetTime         string   `json:"reset_time,omitempty"`
}

type quotaSummaryResponse struct {
	Groups []struct {
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
		Buckets     []struct {
			BucketID          string   `json:"bucketId"`
			Window            string   `json:"window"`
			DisplayName       string   `json:"displayName"`
			Description       string   `json:"description"`
			RemainingFraction *float64 `json:"remainingFraction"`
			ResetTime         string   `json:"resetTime"`
		} `json:"buckets"`
	} `json:"groups"`
}

// RetrieveQuotaSummary 取账号的分组额度（含每周额度）。cred 需带 access token 和项目。
func (c *Client) RetrieveQuotaSummary(ctx context.Context, cred *Credential) (QuotaSummary, error) {
	req := map[string]any{}
	if p := strings.TrimSpace(cred.ProjectID); p != "" {
		req["project"] = p
	}
	var resp quotaSummaryResponse
	if _, err := c.postV1Internal(ctx, cred, "retrieveUserQuotaSummary", req, &resp, nil); err != nil {
		return QuotaSummary{}, err
	}
	out := QuotaSummary{Groups: make([]QuotaGroup, 0, len(resp.Groups))}
	for _, g := range resp.Groups {
		group := QuotaGroup{DisplayName: strings.TrimSpace(g.DisplayName), Description: strings.TrimSpace(g.Description),
			Buckets: make([]QuotaBucket, 0, len(g.Buckets))}
		for _, b := range g.Buckets {
			group.Buckets = append(group.Buckets, QuotaBucket{
				BucketID: strings.TrimSpace(b.BucketID), Window: strings.TrimSpace(b.Window),
				DisplayName: strings.TrimSpace(b.DisplayName), Description: strings.TrimSpace(b.Description),
				RemainingFraction: b.RemainingFraction, ResetTime: strings.TrimSpace(b.ResetTime),
			})
		}
		out.Groups = append(out.Groups, group)
	}
	return out, nil
}

// MarshalQuotaSummary 把分组额度序列化成存库的 JSON。
func MarshalQuotaSummary(s QuotaSummary) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(raw)
}

// UnmarshalQuotaSummary 还原存库的分组额度，空串或解析失败时返回 nil。
func UnmarshalQuotaSummary(raw string) *QuotaSummary {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var s QuotaSummary
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil
	}
	return &s
}
