package antigravity

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
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

// WeeklyLimit 是一个模型所在组的每周额度。Reset 为零值时上游没给重置时间。
type WeeklyLimit struct {
	Remaining float64
	Reset     time.Time
}

// tierSuffix 是显示名末尾的档位，如 "Gemini 4 Neon (High)" 的 " (High)"；"(Argon)" 这类不是档位的保留。
var tierSuffix = regexp.MustCompile(`(?i)\s*\((minimal|low|medium|high|xhigh)\)\s*$`)

// WeeklyLimits 把分组额度里的每周窗口对应到账号目录里的模型：先看组说明里列出的模型名（"Models within this
// group: Gemini 4 Neon, …"，和模型的家族显示名或去掉档位的显示名比对，几个组都列了时取列得最少、最具体的组）；
// 对不上的按厂商归组：gemini* 归 Gemini 的组，claude*、gpt* 归 Claude 和 GPT 的组（同厂商有几个组时取列得最多的
// 总组）。组说明里没列官方客户端的辅助模型（gemini-3-flash 等），但它们的 5 小时额度和同厂商的模型同步变化，
// 按同一个额度池算。没有每周窗口的组、对不上组的模型不在结果里。
func WeeklyLimits(s *QuotaSummary, models []ModelInfo) map[string]WeeklyLimit {
	if s == nil {
		return nil
	}
	type group struct {
		limit  WeeklyLimit
		names  map[string]bool
		vendor string
	}
	var groups []group
	for _, g := range s.Groups {
		var weekly *QuotaBucket
		for i := range g.Buckets {
			if strings.EqualFold(g.Buckets[i].Window, "weekly") {
				weekly = &g.Buckets[i]
				break
			}
		}
		if weekly == nil {
			continue
		}
		limit := WeeklyLimit{}
		if weekly.RemainingFraction != nil {
			limit.Remaining = *weekly.RemainingFraction
		} else if weekly.ResetTime == "" {
			continue // 既没比例也没重置时间，读不出
		}
		limit.Reset, _ = time.Parse(time.RFC3339, weekly.ResetTime)
		names := map[string]bool{}
		if _, list, ok := strings.Cut(g.Description, ":"); ok {
			for _, n := range strings.Split(list, ",") {
				if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
					names[n] = true
				}
			}
		}
		label := strings.ToLower(g.DisplayName)
		vendor := ""
		switch {
		case strings.HasPrefix(label, "gemini"):
			vendor = "gemini"
		case strings.Contains(label, "claude"), strings.Contains(label, "gpt"):
			vendor = "claude"
		}
		groups = append(groups, group{limit, names, vendor})
	}
	if len(groups) == 0 {
		return nil
	}
	out := map[string]WeeklyLimit{}
	for _, m := range models {
		name := m.FamilyDisplayName
		if name == "" {
			name = tierSuffix.ReplaceAllString(m.DisplayName, "")
		}
		name = strings.ToLower(strings.TrimSpace(name))
		best := -1
		for i, g := range groups {
			if name != "" && g.names[name] && (best < 0 || len(g.names) < len(groups[best].names)) {
				best = i
			}
		}
		if best < 0 {
			vendor := ""
			switch id := strings.ToLower(m.ID); {
			case strings.HasPrefix(id, "gemini"):
				vendor = "gemini"
			case strings.HasPrefix(id, "claude"), strings.HasPrefix(id, "gpt"):
				vendor = "claude"
			}
			for i, g := range groups {
				if vendor != "" && g.vendor == vendor && (best < 0 || len(g.names) > len(groups[best].names)) {
					best = i
				}
			}
		}
		if best >= 0 {
			out[m.ID] = groups[best].limit
		}
	}
	return out
}
