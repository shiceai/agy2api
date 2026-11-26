package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/pool"
	"github.com/shiceai/agy2api/internal/store"
)

// 会话粘性：同一对话优先用上次的账号。上游的隐式缓存按账号，换号就命中不了；账号变了派生出的会话 ID
// 也会变，绑了代理时还会在几个出口之间跳。
//
// 对话按"API Key + 请求的模型名 + 会话标识"认：客户端给了会话标识（Claude Code 的会话头、X-Session-Id、
// OpenAI 的 prompt_cache_key，见 Request.Session）就用它，没有时用首条用户消息（Gemini 客户端每次带完整历史、
// 不带会话标识）。记下最近一次成功服务它的账号。只在内存里，闲置超过 Policy.StickyTTL 失效，最多 affinityLimit
// 个对话，重启后清空（对话重新选号）。

// affinityLimit 是最多记住的对话数。
const affinityLimit = 50_000

// 会话粘性的结果（用量详情里的 affinity.result）。
const (
	AffinityHit      = "hit"      // 沿用上次的账号
	AffinitySwitched = "switched" // 上次的账号这次不能接，换了账号（原因见 pool.Affinity*）
	AffinityNew      = "new"      // 新对话：没有记录，或已经过期
)

type affinityEntry struct {
	accountID int64
	used      time.Time
}

// affinityStore 是对话到账号的记录，可并发使用。
type affinityStore struct {
	mu      sync.Mutex
	entries map[string]affinityEntry
	limit   int
}

func newAffinityStore(limit int) *affinityStore {
	return &affinityStore{entries: map[string]affinityEntry{}, limit: limit}
}

// get 返回对话上次用的账号；没有记录或闲置超过 ttl 时返回 0。
func (s *affinityStore) get(key string, now time.Time, ttl time.Duration) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return 0
	}
	if now.Sub(e.used) > ttl {
		delete(s.entries, key)
		return 0
	}
	return e.accountID
}

// set 记下对话这次用的账号。满了先清过期的，还满就清掉最久没用的十分之一。
func (s *affinityStore) set(key string, accountID int64, now time.Time, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[key]; !ok && len(s.entries) >= s.limit {
		for k, e := range s.entries {
			if now.Sub(e.used) > ttl {
				delete(s.entries, k)
			}
		}
		if len(s.entries) >= s.limit {
			keys := make([]string, 0, len(s.entries))
			for k := range s.entries {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return s.entries[keys[i]].used.Before(s.entries[keys[j]].used) })
			for _, k := range keys[:max(len(keys)/10, 1)] {
				delete(s.entries, k)
			}
		}
	}
	s.entries[key] = affinityEntry{accountID: accountID, used: now}
}

// affinityKey 返回请求所属对话的粘性键和认对话的依据（store.Session*）；既没有会话标识也没有用户消息时为空（不粘）。
// 依据也进哈希，不同来源的标识不会撞在一起。
func affinityKey(req Request) (key, source string) {
	id, source := req.Session, req.SessionSource
	if id == "" {
		id, source = antigravity.FirstUserContent(req.Body), store.SessionFirstMessage
	}
	if id == "" {
		return "", ""
	}
	sum := sha256.Sum256([]byte(caller(req) + "\x00" + req.Model + "\x00" + source + "\x00" + id))
	return hex.EncodeToString(sum[:16]), source
}

// affinityDetail 是用量详情里的会话粘性：prefer 是上次的账号（0 表示新对话），lease 是这次选中的账号，
// source 是认对话的依据。
func (r *Relay) affinityDetail(prefer int64, lease *pool.Lease, source string) *store.UsageAffinity {
	switch {
	case prefer == 0:
		return &store.UsageAffinity{Result: AffinityNew, Source: source}
	case lease.Account.ID == prefer:
		return &store.UsageAffinity{Result: AffinityHit, Source: source}
	}
	d := &store.UsageAffinity{Result: AffinitySwitched, Reason: lease.Affinity, FromAccountID: prefer, Source: source}
	for _, a := range r.pool.Accounts() {
		if a.ID == prefer {
			d.FromAccount = a.Email
		}
	}
	return d
}
