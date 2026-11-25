package antigravity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Google 登录添加账号：管理端生成授权链接，用户在自己的浏览器里登录并同意授权，浏览器跳到回环地址
// （打不开，不需要回调服务），用户把地址栏里的地址粘贴回来，服务端用其中的授权码换令牌。
// 服务部署在远端也能用。
//
// 官方客户端的授权请求没有实抓：scope 取真实令牌里实际授予的那组，授权页由用户自己的浏览器访问；
// 换令牌和刷新令牌一样不设 User-Agent（Go 默认）。

const authorizationURL = "https://accounts.google.com/o/oauth2/v2/auth"

// oauthScopes 是真实 Antigravity 令牌里授予的 scope。
var oauthScopes = []string{
	"openid",
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// CodeChallenge 返回 PKCE（S256）的 code_challenge。
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizationURL 构造授权链接：内置的官方 OAuth client，离线访问（拿 refresh token），每次都显示
// 同意页（否则已授权过的账号不再下发 refresh token），带 state 和 PKCE。
func (c *Client) AuthorizationURL(redirectURI, state, verifier string) string {
	q := url.Values{
		"client_id":              {c.defaultID},
		"redirect_uri":           {redirectURI},
		"response_type":          {"code"},
		"scope":                  {strings.Join(oauthScopes, " ")},
		"access_type":            {"offline"},
		"prompt":                 {"consent"},
		"include_granted_scopes": {"true"},
		"state":                  {state},
		"code_challenge":         {CodeChallenge(verifier)},
		"code_challenge_method":  {"S256"},
	}
	return authorizationURL + "?" + q.Encode()
}

// ExchangeCode 经 proxy 用授权码换令牌，返回带 access token、refresh token、client 和代理的凭据
// （还没有身份信息，接着调 Identify）。
func (c *Client) ExchangeCode(ctx context.Context, proxy, code, redirectURI, verifier string) (Credential, error) {
	form := url.Values{
		"client_id":     {c.defaultID},
		"client_secret": {c.defaultSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Credential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// 不设 User-Agent，和令牌刷新一致（官方客户端实抓是 Go 默认）。

	var tok tokenResponse
	if _, err := c.doJSON(proxy, req, &tok); err != nil {
		return Credential{}, fmt.Errorf("oauth: code exchange failed: %w", err)
	}
	cred := Credential{
		AccessToken:  strings.TrimSpace(tok.AccessToken),
		RefreshToken: strings.TrimSpace(tok.RefreshToken),
		IDToken:      strings.TrimSpace(tok.IDToken),
		Scope:        strings.TrimSpace(tok.Scope),
		ClientID:     c.defaultID,
		ClientSecret: c.defaultSecret,
		Proxy:        proxy,
	}
	if cred.AccessToken == "" {
		return Credential{}, fmt.Errorf("oauth: code exchange returned no access_token")
	}
	if cred.RefreshToken == "" {
		return Credential{}, fmt.Errorf("oauth: code exchange returned no refresh_token")
	}
	if tok.ExpiresIn > 0 {
		cred.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	return cred, nil
}
