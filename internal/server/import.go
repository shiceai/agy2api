package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/relay"
	"github.com/shiceai/agy2api/internal/store"
)

// importTimeout 是一次导入（确认身份、取项目、取模型目录）的上限。
const importTimeout = 60 * time.Second

// importResult 是一次导入（粘贴凭据或 Google 登录）的结果。
type importResult struct {
	Account accountView `json:"account"`
	// Created 为 false 表示邮箱已存在，换上了新凭据并重新同步。
	Created bool `json:"created"`
	// Reenabled 表示账号之前因 refresh token 失效被自动停用，换上新凭据后重新启用了。
	Reenabled bool   `json:"reenabled"`
	Warning   string `json:"warning,omitempty"`
}

// importCredential 用一份凭据添加账号：先确认身份拿到邮箱，新邮箱建账号；已有邮箱换上新凭据并重新同步，
// 沿用保存的设备画像（Cloud Code 请求的 User-Agent）、用户项目和绑定的代理。因 refresh token 失效被自动停用的账号
// 同时重新启用；手动停用、条款封禁的保持停用。
// proxy 是新账号要绑定的代理（nil 为直连），确认身份就经它出网；已有账号确认身份后改用原来绑定的代理。
func (s *Service) importCredential(ctx context.Context, cred antigravity.Credential, proxy *store.Proxy) (importResult, error) {
	var proxyID int64
	if proxy != nil {
		proxyID, cred.Proxy = proxy.ID, proxy.URL
	}
	if err := s.client.Identify(ctx, &cred); err != nil {
		return importResult{}, err
	}
	existing, err := s.store.GetAccountByEmail(ctx, s.enc, cred.Email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return importResult{}, err
	}
	if existing != nil {
		cred.UserAgent = existing.UserAgent
		if cred.UserProjectID == "" {
			cred.UserProjectID = existing.UserProjectID
		}
		cred.Proxy = existing.ProxyURL
	}
	res, err := s.client.SyncProject(ctx, cred)
	if err != nil {
		return importResult{}, err
	}
	c := res.Credential
	acc := &store.Account{
		Email:            c.Email,
		Name:             c.Name,
		ProjectID:        c.ProjectID,
		UserProjectID:    c.UserProjectID,
		RefreshToken:     c.RefreshToken,
		ClientID:         c.ClientID,
		ClientSecret:     c.ClientSecret,
		Scope:            c.Scope,
		UserAgent:        c.UserAgent,
		Status:           store.StatusEnabled,
		ModelsJSON:       antigravity.MarshalModels(res.Models),
		SyncedAt:         time.Now().UnixMilli(),
		SubscriptionJSON: antigravity.MarshalSubscription(res.Subscription),
		ProxyID:          proxyID,
	}
	out := importResult{Warning: res.Warning}
	if existing == nil {
		if err := s.store.CreateAccount(ctx, s.enc, acc); err != nil {
			return importResult{}, err
		}
		out.Created = true
		slog.InfoContext(ctx, "已导入账号", "account_id", acc.ID, "email", acc.Email, "models", len(res.Models), "warning", res.Warning)
	} else {
		acc.ID = existing.ID
		if err := s.store.UpdateAccountSync(ctx, s.enc, acc); err != nil {
			return importResult{}, err
		}
		if existing.Status == store.StatusDisabled && existing.DisabledReason == relay.ReasonInvalidGrant {
			if err := s.store.SetAccountStatus(ctx, acc.ID, store.StatusEnabled, ""); err != nil {
				return importResult{}, err
			}
			out.Reenabled = true
		}
		// 缓存的 access token 属于旧凭据，下次请求用新凭据刷新。
		s.relay.InvalidateToken(acc.ID)
		slog.InfoContext(ctx, "已更新账号凭据", "account_id", acc.ID, "email", acc.Email, "models", len(res.Models),
			"reenabled", out.Reenabled, "warning", res.Warning)
	}
	s.reloadAccounts(ctx)
	saved, err := s.store.GetAccount(ctx, s.enc, acc.ID)
	if err != nil {
		return importResult{}, err
	}
	out.Account = s.toAccountView(ctx, saved)
	return out, nil
}

// importError 把导入失败写成管理端能直接显示的中文说明。
func importError(w http.ResponseWriter, err error) {
	switch {
	case antigravity.IsInvalidGrant(err):
		adminError(w, http.StatusBadRequest, "refresh token 无效或已失效（invalid_grant）")
	case errors.Is(err, store.ErrConflict):
		adminError(w, http.StatusConflict, "同一个账号正在导入，请稍后再试")
	case antigravity.IsProxyError(err):
		adminError(w, http.StatusBadGateway, proxyFailure(err))
	default:
		adminError(w, http.StatusBadGateway, "同步失败："+err.Error())
	}
}

type importAccountRequest struct {
	RefreshToken  string `json:"refresh_token"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	UserProjectID string `json:"user_project_id"`
	// 兼容导出格式的字段名。
	ProjectID string `json:"project_id"`
	// Proxy 是新账号要绑定的代理，不传为直连。
	Proxy *proxyChoice `json:"proxy"`
}

// adminImportAccount 用粘贴的 refresh token（或导出的凭据 JSON）添加账号，邮箱已存在时更新凭据。
func (s *Service) adminImportAccount(w http.ResponseWriter, r *http.Request) {
	var req importAccountRequest
	if !decodeAdminJSON(w, r, 1<<20, &req) {
		return
	}
	cred := antigravity.Credential{
		RefreshToken:  strings.TrimSpace(req.RefreshToken),
		ClientID:      strings.TrimSpace(req.ClientID),
		ClientSecret:  strings.TrimSpace(req.ClientSecret),
		UserProjectID: strings.TrimSpace(req.UserProjectID),
		ProjectID:     strings.TrimSpace(req.ProjectID),
	}
	if cred.RefreshToken == "" {
		adminError(w, http.StatusBadRequest, "缺少 refresh token")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), importTimeout)
	defer cancel()
	proxy, err := s.resolveProxyChoice(ctx, req.Proxy)
	if err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.importCredential(ctx, cred, proxy)
	if err != nil {
		slog.WarnContext(ctx, "导入账号失败", "err", err)
		importError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
