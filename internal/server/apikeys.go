package server

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/store"
)

// touchInterval 是同一个 Key 写 last_used_at 的最小间隔，避免每个请求都写库。
const touchInterval = time.Minute

// apiKeyCache 是启用 API Key 的内存快照，按哈希索引，入站鉴权和限制检查都不读库。
// 启动时和管理端增删改 Key 落库后整体重新加载。
//
// 设了 Token 上限的 Key 在内存里记本周期已用的 Token：加载时从用量记录算一次，之后每条用量记录
// 落库前累加（见 AddTokens）。一个请求的消耗事先不知道，所以是软上限：进行中的请求不打断，
// 超出后从下一个请求开始拒绝。
type apiKeyCache struct {
	store *store.Store

	// reloadMu 串行化 Reload，保证后发起的重新加载不会被先发起的旧快照覆盖。
	reloadMu sync.Mutex
	snap     atomic.Pointer[keySnapshot]

	touchMu sync.Mutex
	touched map[int64]time.Time // 每个 Key 上次写 last_used_at 的时间
	// touches 跟踪写 last_used_at 的后台协程，关停时等它们写完再关库（见 Service.Close）。
	touches sync.WaitGroup

	usageMu sync.Mutex
	usage   map[int64]*tokenUsage // 设了上限的 Key 本周期已用的 Token
}

// keySnapshot 是启用 Key 的快照：按哈希查（鉴权），按 ID 查（记用量）。
type keySnapshot struct {
	byHash map[string]*store.APIKey
	byID   map[int64]*store.APIKey
}

// tokenUsage 是一个 Key 在 start（周期开始，Unix 毫秒）以来用掉的 Token。
type tokenUsage struct {
	start int64
	used  int64
}

func newAPIKeyCache(st *store.Store) *apiKeyCache {
	return &apiKeyCache{store: st, touched: map[int64]time.Time{}, usage: map[int64]*tokenUsage{}}
}

// Reload 从库重新加载启用的 Key，并从用量记录重算设了上限的 Key 本周期已用的 Token。
func (c *apiKeyCache) Reload(ctx context.Context) error {
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()
	keys, err := c.store.ListEnabledAPIKeys(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	c.usageMu.Lock()
	old := c.usage
	c.usageMu.Unlock()
	usage := map[int64]*tokenUsage{}
	for _, k := range keys {
		if k.TokenLimit <= 0 {
			continue
		}
		start := periodStart(k.TokenPeriod, now).UnixMilli()
		used, err := c.store.APIKeyTokensSince(ctx, k.ID, start)
		if err != nil {
			return err
		}
		// 用量记录是异步落库的，库里可能还缺排队中的几条；周期没变时保留内存里的计数（取较大者）。
		if o := old[k.ID]; o != nil && o.start == start && o.used > used {
			used = o.used
		}
		usage[k.ID] = &tokenUsage{start: start, used: used}
	}
	snap := &keySnapshot{byHash: keys, byID: make(map[int64]*store.APIKey, len(keys))}
	for _, k := range keys {
		snap.byID[k.ID] = k
	}
	c.snap.Store(snap)
	c.usageMu.Lock()
	c.usage = usage
	c.usageMu.Unlock()
	return nil
}

// Lookup 按明文 Key 查启用的 Key 记录。
func (c *apiKeyCache) Lookup(plain string) (*store.APIKey, bool) {
	snap := c.snap.Load()
	if snap == nil {
		return nil, false
	}
	k, ok := snap.byHash[store.HashAPIKey(plain)]
	return k, ok
}

// Touch 记录 Key 被使用。同一个 Key 每 touchInterval 最多异步写一次库，last_used_at 因此最多滞后这么久。
func (c *apiKeyCache) Touch(id int64) {
	now := time.Now()
	c.touchMu.Lock()
	if last, ok := c.touched[id]; ok && now.Sub(last) < touchInterval {
		c.touchMu.Unlock()
		return
	}
	c.touched[id] = now
	c.touchMu.Unlock()

	c.touches.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.store.TouchAPIKey(ctx, id); err != nil {
			slog.WarnContext(ctx, "更新密钥最近使用时间失败", "key_id", id, "err", err)
		}
	})
}

// AddTokens 把一条用量记录的 Token 记到对应 Key 本周期的用量上（没设上限的 Key 不记）。
func (c *apiKeyCache) AddTokens(keyID, tokens int64) {
	if tokens <= 0 {
		return
	}
	snap := c.snap.Load()
	if snap == nil {
		return
	}
	key := snap.byID[keyID]
	if key == nil || key.TokenLimit <= 0 {
		return
	}
	c.usageMu.Lock()
	defer c.usageMu.Unlock()
	if u := c.current(key, time.Now()); u != nil {
		u.used += tokens
	}
}

// QuotaExceeded 报告 Key 本周期的 Token 是否已用完；每天、每月的周期同时返回下次重置的时间。
func (c *apiKeyCache) QuotaExceeded(k *store.APIKey, now time.Time) (bool, time.Time) {
	if k.TokenLimit <= 0 {
		return false, time.Time{}
	}
	c.usageMu.Lock()
	defer c.usageMu.Unlock()
	u := c.current(k, now)
	if u == nil {
		return false, time.Time{}
	}
	return u.used >= k.TokenLimit, periodEnd(k.TokenPeriod, now)
}

// current 返回 Key 本周期的用量，跨周期时清零。调用方持有 usageMu。
func (c *apiKeyCache) current(k *store.APIKey, now time.Time) *tokenUsage {
	u := c.usage[k.ID]
	if u == nil {
		return nil
	}
	if start := periodStart(k.TokenPeriod, now).UnixMilli(); u.start != start {
		slog.Debug("API Key 的 Token 用量进入新周期", "key_id", k.ID, "period", k.TokenPeriod)
		u.start, u.used = start, 0
	}
	return u
}

// periodStart 返回 now 所在周期的开始（按服务器时区）：每天从 0 点、每月从 1 日 0 点起；总量从头算。
func periodStart(period string, now time.Time) time.Time {
	y, m, d := now.Date()
	switch period {
	case store.TokenPeriodDay:
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	case store.TokenPeriodMonth:
		return time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	default:
		return time.UnixMilli(0)
	}
}

// periodEnd 返回 now 所在周期的结束（下一个周期的开始）；总量没有结束，返回零值。
func periodEnd(period string, now time.Time) time.Time {
	start := periodStart(period, now)
	switch period {
	case store.TokenPeriodDay:
		return start.AddDate(0, 0, 1)
	case store.TokenPeriodMonth:
		return start.AddDate(0, 1, 0)
	default:
		return time.Time{}
	}
}

// keyExpired 报告 Key 是否已到期。
func keyExpired(k *store.APIKey, now time.Time) bool {
	return k.ExpiresAt > 0 && now.UnixMilli() >= k.ExpiresAt
}

// keyAllowsModel 报告 Key 是否允许请求这个模型：没有限制时都允许；否则按客户端请求的模型名匹配列表，
// 不分大小写，* 匹配任意个字符。带档位的 ID（如 gemini-3.8-flash-high）在列表里有它的家族名
// （gemini-3.8-flash）时也允许：允许家族就是允许它的各档。
func keyAllowsModel(k *store.APIKey, model string) bool {
	if k == nil || len(k.AllowedModels) == 0 {
		return true
	}
	model = strings.ToLower(model)
	matches := func(name string) bool {
		for _, p := range k.AllowedModels {
			if globMatch(strings.ToLower(p), name) {
				return true
			}
		}
		return false
	}
	if matches(model) {
		return true
	}
	if t := antigravity.TierOf(model); t != "" {
		return matches(strings.TrimSuffix(model, "-"+t))
	}
	return false
}

// globMatch 匹配只含 * 通配符的模式。
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, last)
}
