package antigravity

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// refreshSkew 是控制面同步（Sync）判断令牌是否要刷新的提前量；数据面按 RefreshPolicy。
const refreshSkew = 5 * time.Minute

const (
	// refreshTimeout 是一次令牌刷新的超时。刷新不随发起请求取消：同账号的其它请求在等它的结果。
	refreshTimeout = 30 * time.Second
	// refreshFailureBackoff 内不重复刷新失败过的账号，直接返回上次的错误，避免并发请求反复打令牌端点。
	refreshFailureBackoff = 5 * time.Second
)

func (c Credential) needsRefresh() bool {
	if c.AccessToken == "" {
		return true
	}
	if c.ExpiresAt.IsZero() {
		return false // 无过期信息时不主动刷新，交由 401 兜底
	}
	return time.Now().Add(refreshSkew).After(c.ExpiresAt)
}

// RefreshPolicy 决定 access token 提前多久刷新。Early 为 false 时只在过期（或上游 401 后被清掉）时刷新；
// 为 true 时提前量在 [MinLead, MaxLead] 之间按账号稳定错开，同批导入的账号不会同时刷新。
type RefreshPolicy struct {
	Early   bool
	MinLead time.Duration
	MaxLead time.Duration
}

// DefaultRefreshPolicy 是未配置时的刷新策略：到期前 5～15 分钟提前刷新。
func DefaultRefreshPolicy() RefreshPolicy {
	return RefreshPolicy{Early: true, MinLead: 5 * time.Minute, MaxLead: 15 * time.Minute}
}

// Lead 返回某账号的提前量，按账号 ID 稳定散开到秒。
func (p RefreshPolicy) Lead(accountID int64) time.Duration {
	if !p.Early {
		return 0
	}
	span := p.MaxLead - p.MinLead
	if span <= 0 {
		return p.MinLead
	}
	h := fnv.New32a()
	fmt.Fprintf(h, "agy2api-refresh-lead:%d", accountID)
	return p.MinLead + time.Duration(uint64(h.Sum32())%uint64(span/time.Second+1))*time.Second
}

// TokenManager 在内存中按账号缓存 access token。同账号的并发刷新合并成一次，等待者共享结果（含失败）；
// refresh token 若发生轮换，通过 onRotate 回调交由上层持久化。
type TokenManager struct {
	client   *Client
	onRotate func(accountID int64, cred Credential)
	policy   atomic.Pointer[RefreshPolicy]

	mu    sync.Mutex
	cache map[int64]*tokenEntry
}

type tokenEntry struct {
	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time     // 零值表示不知道何时过期，靠 401 兜底
	refreshing  chan struct{} // 非 nil 表示正在刷新，刷新结束时关闭
	failErr     error         // 最近一次刷新失败的错误，成功后清空
	failAt      time.Time
}

// usable 报告缓存的令牌是否还没过期；fresh 报告是否还不需要刷新（离过期超过 lead）。
func (e *tokenEntry) usable(now time.Time) bool {
	return e.accessToken != "" && (e.expiresAt.IsZero() || now.Before(e.expiresAt))
}

func (e *tokenEntry) fresh(now time.Time, lead time.Duration) bool {
	return e.usable(now) && (e.expiresAt.IsZero() || now.Add(lead).Before(e.expiresAt))
}

// NewTokenManager 构造令牌缓存，刷新策略为 DefaultRefreshPolicy。onRotate 可为 nil。
func NewTokenManager(client *Client, onRotate func(accountID int64, cred Credential)) *TokenManager {
	m := &TokenManager{client: client, onRotate: onRotate, cache: map[int64]*tokenEntry{}}
	m.SetPolicy(DefaultRefreshPolicy())
	return m
}

// SetPolicy 替换刷新策略，立即生效。
func (m *TokenManager) SetPolicy(p RefreshPolicy) {
	m.policy.Store(&p)
}

// Policy 返回当前刷新策略。
func (m *TokenManager) Policy() RefreshPolicy {
	return *m.policy.Load()
}

func (m *TokenManager) entry(accountID int64) *tokenEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.cache[accountID]
	if e == nil {
		e = &tokenEntry{}
		m.cache[accountID] = e
	}
	return e
}

// AccessToken 返回账号当前可用的 access token，进入提前刷新窗口或已过期时刷新。
// base 必须携带该账号最新的 refresh token 与 client 凭据（通常来自号池快照）。
// 刷新失败但缓存的令牌还没过期时，继续用旧令牌；已过期才返回错误。
func (m *TokenManager) AccessToken(ctx context.Context, accountID int64, base Credential) (string, error) {
	e := m.entry(accountID)
	lead := m.Policy().Lead(accountID)
	for {
		e.mu.Lock()
		now := time.Now()
		if e.fresh(now, lead) {
			tok := e.accessToken
			e.mu.Unlock()
			return tok, nil
		}
		// 已有刷新在进行：等它结束后重新判断，共享它的结果。
		if ch := e.refreshing; ch != nil {
			e.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		// 刚刷新失败过：短时间内不重试。
		if e.failErr != nil && now.Sub(e.failAt) < refreshFailureBackoff {
			tok, usable, err := e.accessToken, e.usable(now), e.failErr
			e.mu.Unlock()
			if usable {
				return tok, nil
			}
			return "", err
		}
		ch := make(chan struct{})
		e.refreshing = ch
		e.mu.Unlock()
		return m.refresh(ctx, accountID, base, e, ch)
	}
}

// refresh 执行一次刷新并更新缓存，结束时关闭 ch 唤醒等待者。
func (m *TokenManager) refresh(ctx context.Context, accountID int64, base Credential, e *tokenEntry, ch chan struct{}) (string, error) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	cred := base
	prevRefresh := cred.RefreshToken
	err := m.client.Refresh(rctx, &cred)
	cancel()

	e.mu.Lock()
	e.refreshing = nil
	close(ch)
	if err != nil {
		e.failErr, e.failAt = err, time.Now()
		tok, usable := e.accessToken, e.usable(time.Now())
		e.mu.Unlock()
		if usable {
			slog.WarnContext(ctx, "刷新 access token 失败，继续用未过期的令牌", "account_id", accountID, "err", err)
			return tok, nil
		}
		slog.WarnContext(ctx, "刷新 access token 失败", "account_id", accountID, "err", err)
		return "", err
	}
	e.failErr = nil
	e.accessToken = cred.AccessToken
	e.expiresAt = cred.ExpiresAt
	tok := e.accessToken
	e.mu.Unlock()

	slog.InfoContext(ctx, "已刷新 access token", "account_id", accountID, "expires_at", cred.ExpiresAt.Format(time.RFC3339))
	if cred.RefreshToken != prevRefresh && m.onRotate != nil {
		m.onRotate(accountID, cred)
	}
	return tok, nil
}

// Invalidate 清除账号缓存的 access token 和失败记录，用于 401 后强制下次刷新。
func (m *TokenManager) Invalidate(accountID int64) {
	e := m.entry(accountID)
	e.mu.Lock()
	e.accessToken = ""
	e.expiresAt = time.Time{}
	e.failErr = nil
	e.mu.Unlock()
}
