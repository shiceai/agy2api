// Package server 组装 HTTP 路由、鉴权中间件、Gemini 入站代理与管理端接口。
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/config"
	"github.com/shiceai/agy2api/internal/cryptox"
	"github.com/shiceai/agy2api/internal/pricing"
	"github.com/shiceai/agy2api/internal/relay"
	"github.com/shiceai/agy2api/internal/store"
	"github.com/shiceai/agy2api/internal/upgrade"
)

// Service 持有各依赖，供 handler 使用。
type Service struct {
	cfg    config.Config
	store  *store.Store
	enc    *cryptox.Cipher
	client *antigravity.Client
	relay  *relay.Relay
	prices *pricing.Service
	keys   *apiKeyCache
	// trusted 是可信反代，adminAllow 是管理端白名单（空为不限），启动时从配置解析（见 clientip.go）。
	trusted    []netip.Prefix
	adminAllow []netip.Prefix
	// guard 记各地址的错误管理口令，太多时锁定（见 adminguard.go）。
	guard *adminGuard
	// build 是版本信息，启动时读一次（见 session.go）。
	build buildInfo
	// upgrade 查新版本、执行在线升级（见 system.go）。
	upgrade *upgrade.Manager
	// aliases 是生效的模型别名规则（见 modelalias.go），零值可用。
	aliases modelAliases
	// clientSystemAsUser 为真时把 Anthropic、Responses 入站客户端的 system 降为用户消息（见 anthropic_compat.go），默认开。
	clientSystemAsUser atomic.Bool
	// oauth 是进行中的 Google 登录会话，零值可用。
	oauth oauthSessions
	// hubVersion 记官方 Antigravity 客户端的最新版本，后台定时查官方更新清单（见 hubversion.go），
	// 落后于内置 UA 版本时在管理端提示；只在后台 goroutine 里写，systemView 读。
	hubVersionMu sync.Mutex
	hubVersion   string
	hubVersionAt time.Time
	// bgStop、bg 控制服务自己的后台任务：代理定时测试（egress.go）、用量记录清理（retention.go）、官方版本检查（hubversion.go）。
	bgStop   chan struct{}
	bg       sync.WaitGroup
	stopOnce sync.Once
	// purging 表示正在清理用量记录（后台和手动不同时跑）。
	purging atomic.Bool
}

// NewService 构造服务，从库加载号池、API Key 缓存、价格和设置，启动后台额度刷新、价格同步、代理出口检测和新版本检查。
// 用完调用 Close。
func NewService(ctx context.Context, cfg config.Config, st *store.Store, enc *cryptox.Cipher) (*Service, error) {
	trusted, err := parsePrefixes(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("AGY2API_TRUSTED_PROXIES: %w", err)
	}
	adminAllow, err := parsePrefixes(cfg.AdminAllow)
	if err != nil {
		return nil, fmt.Errorf("AGY2API_ADMIN_ALLOW: %w", err)
	}
	if len(adminAllow) > 0 {
		slog.Info("管理端只允许这些地址访问", "allow", adminAllow)
	}
	up, err := upgrade.New(upgrade.Options{
		DataDir: cfg.DataDir,
		Version: readBuildInfo(cfg.Version).Version,
		Enabled: cfg.Update,
		API:     cfg.UpdateAPI,
		Backup:  st.BackupTo,
	})
	if err != nil {
		return nil, err
	}
	client := antigravity.NewClient(antigravity.Options{
		TokenURL:      cfg.TokenURL,
		UserInfoURL:   cfg.UserInfoURL,
		UserAgent:     cfg.UserAgent,
		UpstreamBases: cfg.UpstreamBases,
	})
	svc := &Service{
		cfg:     cfg,
		store:   st,
		enc:     enc,
		client:  client,
		relay:   relay.New(client, st, enc),
		prices:  pricing.New(st),
		keys:    newAPIKeyCache(st),
		build:   readBuildInfo(cfg.Version),
		upgrade: up,

		trusted:    trusted,
		adminAllow: adminAllow,
		guard:      newAdminGuard(),

		bgStop: make(chan struct{}),
	}
	// 每条用量记录累加到对应 Key 本周期的 Token 用量上（Token 上限用）。
	svc.relay.SetUsageHook(func(rec *store.UsageRecord) { svc.keys.AddTokens(rec.APIKeyID, rec.TotalTokens) })
	// 用量记录按记录时的模型价格算费用。
	svc.relay.SetPricing(svc.prices)
	if err := svc.prices.Load(ctx); err != nil {
		return nil, err
	}
	if err := svc.relay.Reload(ctx); err != nil {
		return nil, err
	}
	if err := svc.relay.LoadCooldowns(ctx); err != nil {
		return nil, err
	}
	if err := svc.keys.Reload(ctx); err != nil {
		return nil, err
	}
	if err := svc.loadSettings(ctx); err != nil {
		return nil, err
	}
	if err := svc.loadScheduling(ctx); err != nil {
		return nil, err
	}
	if err := svc.loadClientFilter(ctx); err != nil {
		return nil, err
	}
	if err := svc.loadSystemPrompt(ctx); err != nil {
		return nil, err
	}
	if err := svc.loadRequestHandling(ctx); err != nil {
		return nil, err
	}
	if err := svc.loadModelAliases(ctx); err != nil {
		return nil, err
	}
	if err := svc.loadAnthropicCompat(ctx); err != nil {
		return nil, err
	}
	svc.relay.StartQuotaRefresh()
	if cfg.PriceSync {
		svc.prices.Start()
	}
	svc.startEgressChecks()
	svc.startUsagePurge()
	svc.startHubVersionCheck()
	svc.upgrade.Start()
	return svc, nil
}

// Close 释放服务持有的后台资源：停止后台额度刷新、价格同步、代理测试、用量清理和新版本检查，等密钥最近使用时间写完，
// 写完排队的用量记录。
// 应在 HTTP 服务停止后调用。
func (s *Service) Close(ctx context.Context) error {
	s.stopOnce.Do(func() { close(s.bgStop) })
	s.upgrade.Close()
	done := make(chan struct{})
	go func() {
		s.bg.Wait()
		s.keys.touches.Wait() // 请求都结束后不会再有新的，等已发出的写完
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	if err := s.prices.Close(ctx); err != nil {
		slog.ErrorContext(ctx, "停止价格同步超时", "err", err)
	}
	return s.relay.Close(ctx)
}

// reloadAccounts 在账号变更落库后刷新号池快照。
func (s *Service) reloadAccounts(ctx context.Context) {
	reloadSnapshot(ctx, "号池", s.relay.Reload)
}

// reloadKeys 在 API Key 变更落库后刷新鉴权缓存。
func (s *Service) reloadKeys(ctx context.Context) {
	reloadSnapshot(ctx, "API Key 缓存", s.keys.Reload)
}

// reloadSnapshot 刷新一份内存快照。不随请求取消：库已写入，快照必须跟上，
// 否则会按过期状态选号或鉴权。
func reloadSnapshot(ctx context.Context, name string, reload func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := reload(ctx); err != nil {
		slog.ErrorContext(ctx, "刷新"+name+"失败", "err", err)
	}
}

// syncAccount 同步一个账号并回写身份、项目、模型目录。
func (s *Service) syncAccount(ctx context.Context, a *store.Account) (string, error) {
	res, err := s.client.Sync(ctx, relay.Credential(a))
	if err != nil {
		return "", err
	}
	cred := res.Credential
	a.Email = cred.Email
	a.Name = cred.Name
	a.ProjectID = cred.ProjectID
	a.RefreshToken = cred.RefreshToken
	a.ClientID = cred.ClientID
	a.ClientSecret = cred.ClientSecret
	a.Scope = cred.Scope
	a.UserAgent = cred.UserAgent
	a.ModelsJSON = antigravity.MarshalModels(res.Models)
	a.SubscriptionJSON = antigravity.MarshalSubscription(res.Subscription)
	a.SyncedAt = time.Now().UnixMilli()
	if err := s.store.UpdateAccountSync(ctx, s.enc, a); err != nil {
		return "", err
	}
	// refresh token 可能已轮换，清掉缓存的 access token，下次请求用新凭据刷新。
	s.relay.InvalidateToken(a.ID)
	// 开了每周额度的账号顺带拉一次分组额度，失败不影响同步结果。
	if s.relay.SyncQuotaSummary(ctx, a) {
		if fresh, err := s.store.GetAccount(ctx, s.enc, a.ID); err == nil {
			*a = *fresh
		}
	}
	s.reloadAccounts(ctx)
	return res.Warning, nil
}
