package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func asHTTPError(err error, target **HTTPError) bool {
	return errors.As(err, target)
}

// postV1Internal 向 {base}/v1internal:<method> 发送 JSON 并解码响应，基址按控制面策略回退（429 也换，
// 见 basePolicy）。控制面走带整体超时的 HTTP 客户端。
func (c *Client) postV1Internal(ctx context.Context, cred *Credential, method string, body any, out any, extraHeaders map[string]string) (int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	headers := cloudCodeHeaders(cred.AccessToken, c.UserAgentFor(cred.UserAgent, cred.Email))
	for k, v := range extraHeaders {
		headers.Set(k, v)
	}
	rt, err := c.route(cred.Proxy)
	if err != nil {
		return 0, err
	}
	resp, err := c.postWithFallback(ctx, rt.control, controlPlane, method, "", headers, payload)
	if err != nil {
		slog.WarnContext(ctx, "上游控制面请求失败", "method", method, "err", err)
		return 0, err
	}
	status, err := decodeJSON(resp, out)
	if err != nil {
		slog.WarnContext(ctx, "上游控制面请求失败", "method", method, "status", status, "err", err)
	}
	return status, err
}

// --- loadCodeAssist ---

type tierPayload struct {
	ID                                 string `json:"id"`
	Name                               string `json:"name"`
	IsDefault                          bool   `json:"isDefault"`
	UserDefinedCloudaicompanionProject bool   `json:"userDefinedCloudaicompanionProject"`
}

type loadCodeAssistResponse struct {
	ProjectID       string        `json:"cloudaicompanionProject"`
	CurrentTier     *tierPayload  `json:"currentTier"`
	PaidTier        *tierPayload  `json:"paidTier"`
	AllowedTiers    []tierPayload `json:"allowedTiers"`
	IneligibleTiers []struct {
		ReasonCode    string `json:"reasonCode"`
		ReasonMessage string `json:"reasonMessage"`
	} `json:"ineligibleTiers"`
}

// Subscription 是 loadCodeAssist 返回的订阅信息，管理端账号页显示。
type Subscription struct {
	CurrentTier   string `json:"current_tier,omitempty"` // 当前档位名称（没有名称时是 ID）
	CurrentTierID string `json:"current_tier_id,omitempty"`
	PaidTier      string `json:"paid_tier,omitempty"` // 付费档位名称，如 Google AI Pro
	PaidTierID    string `json:"paid_tier_id,omitempty"`
	// Ineligible 是不符合资格的档位原因（reasonCode: reasonMessage）。
	Ineligible []string `json:"ineligible,omitempty"`
}

// Downgraded 报告付费档位是否降到了 Antigravity Starter Quota（订阅失效后的降级档）。
func (s Subscription) Downgraded() bool {
	return strings.Contains(strings.ToLower(s.PaidTier+" "+s.PaidTierID), "starter")
}

// MarshalSubscription 把订阅信息序列化成存库的 JSON。
func MarshalSubscription(s Subscription) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(raw)
}

// UnmarshalSubscription 还原存库的订阅信息，空串或解析失败时返回零值。
func UnmarshalSubscription(raw string) Subscription {
	var s Subscription
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	return s
}

func tierName(t *tierPayload) (name, id string) {
	if t == nil {
		return "", ""
	}
	id = strings.TrimSpace(t.ID)
	name = strings.TrimSpace(t.Name)
	if name == "" {
		name = id
	}
	return name, id
}

func (r loadCodeAssistResponse) subscription() Subscription {
	var s Subscription
	s.CurrentTier, s.CurrentTierID = tierName(r.CurrentTier)
	s.PaidTier, s.PaidTierID = tierName(r.PaidTier)
	for _, t := range r.IneligibleTiers {
		reason := strings.TrimSpace(t.ReasonCode)
		if msg := strings.TrimSpace(t.ReasonMessage); msg != "" {
			reason += ": " + msg
		}
		if reason != "" {
			s.Ineligible = append(s.Ineligible, reason)
		}
	}
	return s
}

// FetchSubscription 调 loadCodeAssist 只取订阅信息（后台定期检查降档用）。cred 需带 access token。
func (c *Client) FetchSubscription(ctx context.Context, cred *Credential) (Subscription, error) {
	_, resp, err := c.LoadCodeAssist(ctx, cred)
	if err != nil {
		return Subscription{}, err
	}
	return resp.subscription(), nil
}

// LoadCodeAssist 取账号的 Cloud Code 项目与 tier。consumer 层会直接返回项目；
// Workspace/企业账号可能返回空项目，需调用方处理（设置 user project 或开通）。
func (c *Client) LoadCodeAssist(ctx context.Context, cred *Credential) (projectID string, tiers loadCodeAssistResponse, err error) {
	metadata := map[string]any{"ideType": "ANTIGRAVITY"}
	req := map[string]any{"metadata": metadata}
	if up := strings.TrimSpace(cred.UserProjectID); up != "" {
		req["cloudaicompanionProject"] = up
		metadata["duetProject"] = up
	}
	var resp loadCodeAssistResponse
	if _, err = c.postV1Internal(ctx, cred, "loadCodeAssist", req, &resp, nil); err != nil {
		return "", loadCodeAssistResponse{}, err
	}
	projectID = strings.TrimSpace(resp.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(cred.UserProjectID)
	}
	return projectID, resp, nil
}

// --- onboardUser ---

type onboardResponse struct {
	Done     bool `json:"done"`
	Response struct {
		Project json.RawMessage `json:"cloudaicompanionProject"`
	} `json:"response"`
}

func (r onboardResponse) projectID() string {
	raw := strings.TrimSpace(string(r.Response.Project))
	if raw == "" || raw == "null" {
		return ""
	}
	var asString string
	if err := json.Unmarshal([]byte(raw), &asString); err == nil {
		return strings.TrimSpace(asString)
	}
	var asObject struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &asObject); err == nil {
		return strings.TrimSpace(asObject.ID)
	}
	return ""
}

func onboardTierID(tiers loadCodeAssistResponse) string {
	for _, t := range tiers.AllowedTiers {
		if t.IsDefault && strings.TrimSpace(t.ID) != "" {
			return strings.TrimSpace(t.ID)
		}
	}
	if tiers.CurrentTier != nil && strings.TrimSpace(tiers.CurrentTier.ID) != "" {
		return strings.TrimSpace(tiers.CurrentTier.ID)
	}
	return onboardDefaultTier
}

// OnboardUser 为从未用过 Antigravity 的账号开通 Cloud Code 项目，轮询至完成。
func (c *Client) OnboardUser(ctx context.Context, cred *Credential, tiers loadCodeAssistResponse) (string, error) {
	tierID := onboardTierID(tiers)
	metadata := map[string]any{
		"ideType":    "ANTIGRAVITY",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}
	payload := map[string]any{"tierId": tierID, "metadata": metadata}
	// 免费层跑在 Google 托管项目上，指定项目会导致 FAILED_PRECONDITION。
	if up := strings.TrimSpace(cred.UserProjectID); up != "" && tierID != onboardDefaultTier {
		payload["cloudaicompanionProject"] = up
		metadata["duetProject"] = up
	}
	headers := map[string]string{"X-Goog-Api-Client": onboardAPIClient}

	var lastErr error
	for attempt := 0; attempt < onboardPollAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(onboardPollInterval):
			}
		}
		var resp onboardResponse
		status, err := c.postV1Internal(ctx, cred, "onboardUser", payload, &resp, headers)
		if err != nil {
			lastErr = err
			if status != http.StatusTooManyRequests && status != 0 && status < 500 {
				break
			}
			continue
		}
		if pid := resp.projectID(); pid != "" {
			return pid, nil
		}
		if !resp.Done {
			lastErr = errors.New("onboardUser operation still pending")
			continue
		}
		if up := strings.TrimSpace(cred.UserProjectID); up != "" {
			return up, nil
		}
		lastErr = errors.New("onboardUser finished without a project; Workspace/企业账号需在账号上设置 GCP project ID")
		break
	}
	if lastErr == nil {
		lastErr = errors.New("onboardUser did not return a project id")
	}
	return "", lastErr
}
