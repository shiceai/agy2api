// Package antigravity 是 Antigravity（Google Cloud Code 内部接口）来源的全部上游交互。
//
// 控制面：OAuth 令牌刷新、身份确认、loadCodeAssist 取项目、onboardUser 开通项目、
// fetchAvailableModels 取模型目录与额度。
// 数据面：构造 v1internal 信封、generateContent / streamGenerateContent / countTokens 调用、
// SSE 解包、429 解析。
//
// 所有上游请求都经由 Client 发出。Cloud Code（v1internal）请求必须带真实 Antigravity User-Agent，
// 否则上游返回 403 SUBSCRIPTION_REQUIRED；OAuth 令牌刷新按官方客户端实抓用 Go 默认 User-Agent。
package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const (
	responseBodyLimit = 8 << 20
	// onboardUser 返回长运行操作，通常一两次轮询即完成。
	onboardPollAttempts = 5
	onboardPollInterval = 2 * time.Second
	onboardDefaultTier  = "free-tier"
	// onboardAPIClient 对齐官方 Node 客户端在开通调用上发送的 X-Goog-Api-Client。
	onboardAPIClient = "gl-node/22.21.1"
)

// DefaultUserAgent 返回最新核验构建（活动表表顶）的 User-Agent，账号未知时用。
// 真实 Antigravity 桌面端对 Cloud Code 声明的 User-Agent 形态：
// antigravity/hub/<ver> (aidev_client; os_type=<os>; arch=<arch>; cl=<构建号>)。
// 上游据此判定调用方是否为受支持的客户端，格式不对或缺失会被判为无授权，
// 返回 SUBSCRIPTION_REQUIRED。活动表可被远程刷新（见 builds.go），故取函数、每次读当前表顶。
func DefaultUserAgent() string { return userAgentFor(currentBuilds()[0]) }

// 上游默认值，Options 对应字段留空时使用。
const (
	DefaultTokenURL    = "https://oauth2.googleapis.com/token"
	DefaultUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"

	// 官方 Antigravity 桌面端内置的 installed-app OAuth 客户端，社区普遍当公开凭据使用；
	// 导入的 refresh token 未自带 client 时用它刷新。
	defaultOAuthClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	defaultOAuthClientSecret = "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"
)

// DefaultUpstreamBases 是生产的 daily→prod 回退顺序。
var DefaultUpstreamBases = []string{
	"https://daily-cloudcode-pa.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

// Client 是无状态的上游客户端，控制面和数据面共用。令牌缓存见 TokenManager。
type Client struct {
	direct        route    // 直连（或进程的 HTTPS_PROXY 环境变量）
	routes        sync.Map // 代理地址 → *route，见 proxy.go
	tokenURL      string
	userInfoURL   string
	uaOverride    string   // 配置了统一 UA 时所有账号都用它，空表示按账号派生
	upstreamBases []string // {base}/v1internal:<method>
	defaultID     string   // 内置官方 OAuth client
	defaultSecret string
}

// Options 覆盖上游默认值，留空的字段用对应的 Default* 值。联调和测试时指向 mock。
type Options struct {
	TokenURL      string
	UserInfoURL   string
	UserAgent     string // 统一 UA，空表示每个账号按 DeviceUserAgent 派生
	UpstreamBases []string
}

// NewClient 构造一个上游客户端。
func NewClient(o Options) *Client {
	bases := o.UpstreamBases
	if len(bases) == 0 {
		bases = DefaultUpstreamBases
	}
	return &Client{
		direct: route{
			control: &http.Client{Timeout: 30 * time.Second},
			data:    &http.Client{Transport: newDataTransport(http.ProxyFromEnvironment)},
		},
		tokenURL:      orDefault(o.TokenURL, DefaultTokenURL),
		userInfoURL:   orDefault(o.UserInfoURL, DefaultUserInfoURL),
		uaOverride:    o.UserAgent,
		upstreamBases: append([]string(nil), bases...),
		defaultID:     defaultOAuthClientID,
		defaultSecret: defaultOAuthClientSecret,
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// UserAgentFor 返回某账号 Cloud Code 请求实际用的 User-Agent，优先级：配置的统一 UA >
// 账号保存的设备画像 stored > 按账号邮箱派生（见 DeviceUserAgent）> 最新核验构建（账号未知时，
// 如导入时还没拿到邮箱）。
func (c *Client) UserAgentFor(stored, accountKey string) string {
	switch {
	case c.uaOverride != "":
		return c.uaOverride
	case stored != "":
		return stored
	case strings.TrimSpace(accountKey) != "":
		return DeviceUserAgent(accountKey)
	default:
		return DefaultUserAgent()
	}
}

// Credential 是一个账号的凭据与同步出来的身份/项目信息。
type Credential struct {
	// UserAgent 是账号保存的设备画像（Cloud Code 请求用的 User-Agent），空表示还没有；
	// 实际发送的值见 Client.UserAgentFor。
	UserAgent     string
	AccessToken   string
	RefreshToken  string
	IDToken       string
	Email         string
	Name          string
	ProjectID     string
	UserProjectID string
	ClientID      string
	ClientSecret  string
	Scope         string
	ExpiresAt     time.Time
	// Proxy 是账号绑定的代理地址，这个账号的所有请求都经它出网；空表示直连。见 proxy.go。
	Proxy string
}

func (c *Client) clientCreds(cred Credential) (id, secret string) {
	id, secret = strings.TrimSpace(cred.ClientID), strings.TrimSpace(cred.ClientSecret)
	if id == "" || secret == "" {
		return c.defaultID, c.defaultSecret
	}
	return id, secret
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
}

// Refresh 用 refresh token 换取新的 access token，必要时轮换 refresh token。
func (c *Client) Refresh(ctx context.Context, cred *Credential) error {
	rt := strings.TrimSpace(cred.RefreshToken)
	if rt == "" {
		return fmt.Errorf("oauth: account has no refresh token")
	}
	id, secret := c.clientCreds(*cred)
	form := url.Values{
		"client_id":     {id},
		"client_secret": {secret},
		"refresh_token": {rt},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// 不设 User-Agent：官方客户端 2.28.2、2.31.0 实抓的令牌刷新都是 Go 默认的 Go-http-client/2.0。

	var tok tokenResponse
	if _, err := c.doJSON(cred.Proxy, req, &tok); err != nil {
		return fmt.Errorf("oauth: refresh failed: %w", err)
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return fmt.Errorf("oauth: refresh returned no access_token")
	}
	cred.AccessToken = strings.TrimSpace(tok.AccessToken)
	if strings.TrimSpace(tok.RefreshToken) != "" {
		cred.RefreshToken = strings.TrimSpace(tok.RefreshToken)
	}
	cred.IDToken = strings.TrimSpace(tok.IDToken)
	if strings.TrimSpace(tok.Scope) != "" {
		cred.Scope = strings.TrimSpace(tok.Scope)
	}
	if tok.ExpiresIn > 0 {
		cred.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	// 把成功的 client 凭据固化到账号，保证下次刷新一致。
	cred.ClientID, cred.ClientSecret = id, secret
	return nil
}

// Profile 是用户身份信息。
type Profile struct {
	Email         string `json:"email"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	VerifiedEmail bool   `json:"verified_email"`
}

// FetchProfile 经 proxy 读取 Google 用户信息，确认账号身份。
func (c *Client) FetchProfile(ctx context.Context, proxy, accessToken string) (Profile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.userInfoURL, nil)
	if err != nil {
		return Profile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", c.UserAgentFor("", ""))
	var p Profile
	if _, err := c.doJSON(proxy, req, &p); err != nil {
		return Profile{}, err
	}
	p.Email = strings.TrimSpace(p.Email)
	if p.Email == "" {
		return Profile{}, fmt.Errorf("oauth: profile has no email")
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = p.Email
	}
	return p, nil
}

// doJSON 经 proxy 用控制面客户端执行请求并解码 JSON 响应，返回 HTTP 状态码。非 2xx 视为错误（*HTTPError）。
func (c *Client) doJSON(proxy string, req *http.Request, out any) (int, error) {
	rt, err := c.route(proxy)
	if err != nil {
		return 0, err
	}
	resp, err := rt.control.Do(req)
	if err != nil {
		return 0, err
	}
	return decodeJSON(resp, out)
}

// decodeJSON 读完并关闭响应，2xx 时把 JSON 解码进 out（可为 nil），非 2xx 返回 *HTTPError。
func decodeJSON(resp *http.Response, out any) (int, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, responseBodyLimit))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, &HTTPError{Status: resp.StatusCode, Body: string(body)}
	}
	if out != nil && len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// HTTPError 携带上游返回的状态码与截断后的响应体。
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	body := e.Body
	if len(body) > 300 {
		body = body[:300]
	}
	return fmt.Sprintf("upstream HTTP %d: %s", e.Status, body)
}

// IsUnauthorized 报告错误是否为 401。
func IsUnauthorized(err error) bool {
	var he *HTTPError
	if ok := asHTTPError(err, &he); ok {
		return he.Status == http.StatusUnauthorized
	}
	return false
}

// IsInvalidGrant 报告令牌刷新失败是否因为 refresh token 已失效（被撤销、过期、改了密码等），
// 这种情况重试没用，账号应停用。只认 OAuth 错误码，不把普通的 400/401 或网络错误当成失效。
func IsInvalidGrant(err error) bool {
	var he *HTTPError
	if !asHTTPError(err, &he) {
		return false
	}
	switch gjson.Get(he.Body, "error").String() {
	case "invalid_grant", "invalid_refresh_token", "refresh_token_expired":
		return true
	}
	return false
}
