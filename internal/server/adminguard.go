package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// 管理口令防爆破：按客户端地址统计"不同的"错误口令，超过上限就锁定这个地址的管理接口。
//
//   - 只数不同的口令：前端每个请求都带口令，服务端改了口令后没关的页面还会拿旧口令请求几次，按请求数算会把管理员
//     自己锁住。同一个错误口令重复多少次都只算一次；猜口令每次换一个值，照样计数。
//   - 锁定期间口令对了也不放行，否则攻击者在锁定期间照样能试出口令。
//   - 口令正确不清零：和管理员共用出口（同一个 NAT）的攻击者，计数会被管理员页面的轮询不断清零。计数靠窗口自然过期。
//   - IPv6 按 /64 合并：一台主机通常能拿到整个 /64，可以换着地址试。
//   - 不做全局锁：那样攻击者能把管理员锁在外面。
//   - 只在内存里，重启清空；错误口令只存带进程随机密钥的 HMAC，不存明文。
//
// 计数依赖客户端 IP 识别正确（见 clientip.go）：反代不在可信列表里时，所有请求都像来自反代，一个人猜错会锁住所有人。
const (
	guardWindow    = 15 * time.Minute // 统计不同错误口令的时间窗口
	guardThreshold = 10               // 窗口内不同的错误口令达到这个数就锁定
	guardBaseLock  = 15 * time.Minute // 第一次锁定的时长，之后每次翻倍
	guardMaxLock   = 24 * time.Hour   // 锁定时长上限
	guardForget    = 24 * time.Hour   // 解锁后这么久没有新的错误口令，忘掉这个地址（锁定次数清零）
	guardCapacity  = 10000            // 最多记这么多个地址，满了先淘汰最久没有错误口令的
)

type adminGuard struct {
	mu       sync.Mutex
	key      []byte // 进程启动时随机生成的 HMAC 密钥
	capacity int
	records  map[netip.Prefix]*guardRecord
}

type guardRecord struct {
	fails       map[[sha256.Size]byte]time.Time // 窗口内的错误口令（HMAC）→ 最近一次出现的时间
	strikes     int                             // 已经锁定过几次，决定下次锁多久
	lockedUntil time.Time
	lastFail    time.Time
}

// idleSince 是这个地址最后一次"有事"的时间：最近一次错误口令和锁定结束取较晚者。锁定期间的请求直接被拒、不记错误，
// 所以"多久没有新的错误口令"从解锁算起，否则锁满 24 小时一解锁就被忘掉，又从 15 分钟锁起。
func (r *guardRecord) idleSince() time.Time {
	if r.lockedUntil.After(r.lastFail) {
		return r.lockedUntil
	}
	return r.lastFail
}

func newAdminGuard() *adminGuard {
	key := make([]byte, 32)
	_, _ = rand.Read(key) // crypto/rand 在受支持的平台上不会失败
	return &adminGuard{key: key, capacity: guardCapacity, records: map[netip.Prefix]*guardRecord{}}
}

// guardKey 返回计数用的地址：IPv4 按单个地址，IPv6 按 /64。解析不了的地址都落到零值上，一起计数。
func guardKey(a netip.Addr) netip.Prefix {
	switch {
	case a.Is4():
		return netip.PrefixFrom(a, 32)
	case a.Is6():
		p, _ := a.Prefix(64)
		return p
	}
	return netip.Prefix{}
}

// Locked 报告这个地址是否在锁定中，以及还要等多久。
func (g *adminGuard) Locked(a netip.Addr, now time.Time) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if rec := g.records[guardKey(a)]; rec != nil && now.Before(rec.lockedUntil) {
		return rec.lockedUntil.Sub(now), true
	}
	return 0, false
}

// Fail 记一次错误口令。这次让窗口内不同的错误口令达到上限时锁定这个地址，返回锁定时长（否则为 0），
// distinct 是计入的不同错误口令数。
func (g *adminGuard) Fail(a netip.Addr, password string, now time.Time) (lock time.Duration, distinct int) {
	mac := hmac.New(sha256.New, g.key)
	mac.Write([]byte(password))
	var sum [sha256.Size]byte
	copy(sum[:], mac.Sum(nil))

	g.mu.Lock()
	defer g.mu.Unlock()
	k := guardKey(a)
	rec := g.records[k]
	if rec == nil || now.Sub(rec.idleSince()) >= guardForget {
		if rec == nil && len(g.records) >= g.capacity {
			g.evict(now)
		}
		rec = &guardRecord{fails: map[[sha256.Size]byte]time.Time{}}
		g.records[k] = rec
	}
	for h, at := range rec.fails {
		if now.Sub(at) >= guardWindow {
			delete(rec.fails, h)
		}
	}
	rec.fails[sum] = now
	rec.lastFail = now
	distinct = len(rec.fails)
	if distinct < guardThreshold {
		return 0, distinct
	}
	rec.strikes++
	lock = guardBaseLock << min(rec.strikes-1, 16) // 移位次数封顶，防溢出；实际由 guardMaxLock 截住
	if lock > guardMaxLock || lock <= 0 {
		lock = guardMaxLock
	}
	rec.lockedUntil = now.Add(lock)
	clear(rec.fails) // 解锁后重新数
	return lock, distinct
}

// evict 在记录满时腾出位置：先删已经忘掉的地址，还是满的就删最久没有错误口令的那个。调用方持有锁。
func (g *adminGuard) evict(now time.Time) {
	var oldest netip.Prefix
	var oldestAt time.Time
	for k, rec := range g.records {
		if now.Sub(rec.idleSince()) >= guardForget {
			delete(g.records, k)
			continue
		}
		if oldestAt.IsZero() || rec.lastFail.Before(oldestAt) {
			oldest, oldestAt = k, rec.lastFail
		}
	}
	if len(g.records) >= g.capacity {
		delete(g.records, oldest)
	}
}

// adminLocked 回复锁定中：429，Retry-After 是还要等的秒数，code 供前端显示中文说明。
func adminLocked(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error": "too many wrong admin passwords from this address, try again later",
		"code":  "admin_locked",
	})
}
