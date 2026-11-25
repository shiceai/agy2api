package antigravity

import (
	"context"
	"fmt"
	"strings"
)

// SyncResult 汇总一次账号同步的产出。
type SyncResult struct {
	Credential Credential
	Models     []ModelInfo
	// Subscription 是 loadCodeAssist 返回的订阅信息。
	Subscription Subscription
	Warning      string
}

// Sync 完成一个账号的完整同步：确认身份（Identify），再同步项目和模型目录（SyncProject）。
func (c *Client) Sync(ctx context.Context, cred Credential) (SyncResult, error) {
	if err := c.Identify(ctx, &cred); err != nil {
		return SyncResult{Credential: cred}, err
	}
	return c.SyncProject(ctx, cred)
}

// Identify 在首次或令牌将过期时刷新令牌，再读取 Google 身份，填好 cred 的邮箱和名称；
// 401 会触发一次刷新重试。导入时先用它拿到邮箱，判断是新账号还是已有账号（已有账号沿用保存的
// 设备画像），再调 SyncProject。
func (c *Client) Identify(ctx context.Context, cred *Credential) error {
	if strings.TrimSpace(cred.RefreshToken) == "" && strings.TrimSpace(cred.AccessToken) == "" {
		return fmt.Errorf("oauth: credential has neither access nor refresh token")
	}
	if cred.AccessToken == "" || cred.needsRefresh() {
		if err := c.Refresh(ctx, cred); err != nil {
			return err
		}
	}
	profile, err := c.FetchProfile(ctx, cred.Proxy, cred.AccessToken)
	if err != nil && IsUnauthorized(err) && cred.RefreshToken != "" {
		if rErr := c.Refresh(ctx, cred); rErr != nil {
			return rErr
		}
		profile, err = c.FetchProfile(ctx, cred.Proxy, cred.AccessToken)
	}
	if err != nil {
		return err
	}
	cred.Email, cred.Name = profile.Email, profile.Name
	return nil
}

// SyncProject 在已确认身份的凭据上：派生设备画像 → loadCodeAssist 取项目（必要时 onboardUser 开通）
// → 取模型目录和订阅信息。
func (c *Client) SyncProject(ctx context.Context, cred Credential) (SyncResult, error) {
	// 拿到邮箱后为新账号派生设备画像（已有画像的保持不变），后续 Cloud Code 请求用它。
	// 保存的是账号自己的画像，不受统一 UA 配置影响，取消统一 UA 后仍稳定。
	if cred.UserAgent == "" {
		cred.UserAgent = DeviceUserAgent(cred.Email)
	}

	projectID, tiers, err := c.LoadCodeAssist(ctx, &cred)
	if err != nil {
		return SyncResult{Credential: cred}, fmt.Errorf("loadCodeAssist: %w", err)
	}
	if projectID == "" {
		onboarded, oErr := c.OnboardUser(ctx, &cred, tiers)
		if oErr != nil {
			return SyncResult{Credential: cred}, fmt.Errorf("onboardUser: %w", oErr)
		}
		projectID = onboarded
	}
	cred.ProjectID = projectID

	models, err := c.FetchAvailableModels(ctx, &cred)
	result := SyncResult{Credential: cred, Models: models, Subscription: tiers.subscription()}
	if err != nil {
		// 模型目录拿不到不致命：账号已有项目仍可尝试调度，记为告警。
		result.Warning = "fetchAvailableModels failed: " + err.Error()
		return result, nil
	}
	return result, nil
}
