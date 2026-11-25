package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/config"
	"github.com/shiceai/agy2api/internal/mockupstream"
)

var guardT0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// failDistinct 从 a 连续提交 n 个不同的错误口令（前缀区分批次），返回最后一次的锁定时长。
func failDistinct(g *adminGuard, a netip.Addr, prefix string, n int, now time.Time) time.Duration {
	var lock time.Duration
	for i := 0; i < n; i++ {
		lock, _ = g.Fail(a, fmt.Sprintf("%s-%d", prefix, i), now)
	}
	return lock
}

// 同一个错误口令重复多少次都只算一次：改了口令后没关的页面拿旧口令轮询，不会把管理员锁住。
func TestAdminGuardCountsDistinctPasswords(t *testing.T) {
	g := newAdminGuard()
	a := netip.MustParseAddr("203.0.113.7")
	for i := 0; i < 100; i++ {
		if lock, distinct := g.Fail(a, "old-password", guardT0.Add(time.Duration(i)*3*time.Second)); lock != 0 || distinct != 1 {
			t.Fatalf("第 %d 次重复同一个错误口令：lock=%v distinct=%d，应不锁定、只算 1 个", i+1, lock, distinct)
		}
	}
}

// 窗口内第 10 个不同的错误口令触发锁定 15 分钟，到期自动解锁。
func TestAdminGuardLocksAtThreshold(t *testing.T) {
	g := newAdminGuard()
	a := netip.MustParseAddr("203.0.113.7")
	if lock := failDistinct(g, a, "guess", guardThreshold-1, guardT0); lock != 0 {
		t.Fatalf("%d 个不同的错误口令不应锁定，实际 %v", guardThreshold-1, lock)
	}
	if _, locked := g.Locked(a, guardT0); locked {
		t.Fatal("还没到上限就锁定了")
	}
	lock, distinct := g.Fail(a, "guess-last", guardT0)
	if lock != guardBaseLock || distinct != guardThreshold {
		t.Fatalf("第 %d 个不同的错误口令应锁定 %v，实际 lock=%v distinct=%d", guardThreshold, guardBaseLock, lock, distinct)
	}
	if wait, locked := g.Locked(a, guardT0.Add(time.Minute)); !locked || wait != guardBaseLock-time.Minute {
		t.Fatalf("锁定 1 分钟后应还剩 %v，实际 locked=%v wait=%v", guardBaseLock-time.Minute, locked, wait)
	}
	if _, locked := g.Locked(a, guardT0.Add(guardBaseLock)); locked {
		t.Fatal("锁定到期后应解锁")
	}
}

// 超过窗口的错误口令不再计数。
func TestAdminGuardWindowExpires(t *testing.T) {
	g := newAdminGuard()
	a := netip.MustParseAddr("203.0.113.7")
	failDistinct(g, a, "early", guardThreshold-1, guardT0)
	if lock, distinct := g.Fail(a, "late", guardT0.Add(guardWindow)); lock != 0 || distinct != 1 {
		t.Fatalf("窗口外的错误口令应已过期：lock=%v distinct=%d", lock, distinct)
	}
}

// 解锁后再犯，锁定时长翻倍，最长 24 小时；24 小时没有新的错误口令后重新从 15 分钟算。
func TestAdminGuardEscalatesAndForgets(t *testing.T) {
	g := newAdminGuard()
	a := netip.MustParseAddr("203.0.113.7")
	now := guardT0
	want := []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for i, w := range want {
		lock := failDistinct(g, a, fmt.Sprintf("round%d", i), guardThreshold, now)
		if lock != w {
			t.Fatalf("第 %d 次锁定应为 %v，实际 %v", i+1, w, lock)
		}
		now = now.Add(lock) // 等到解锁再试
	}
	now = now.Add(guardForget)
	if lock := failDistinct(g, a, "fresh", guardThreshold, now); lock != guardBaseLock {
		t.Fatalf("24 小时没有错误口令后应从 %v 重新算，实际 %v", guardBaseLock, lock)
	}
}

// IPv6 按 /64 合并计数；IPv4 按单个地址，互不影响。
func TestAdminGuardAddressKeys(t *testing.T) {
	g := newAdminGuard()
	for i := 0; i < guardThreshold; i++ {
		g.Fail(netip.MustParseAddr(fmt.Sprintf("2001:db8:1:2::%x", i+1)), fmt.Sprintf("guess-%d", i), guardT0)
	}
	if _, locked := g.Locked(netip.MustParseAddr("2001:db8:1:2:ffff::1"), guardT0); !locked {
		t.Fatal("同一个 /64 里换地址猜口令，应一起计数并锁定整个 /64")
	}
	if _, locked := g.Locked(netip.MustParseAddr("2001:db8:1:3::1"), guardT0); locked {
		t.Fatal("别的 /64 不应受影响")
	}
	failDistinct(g, netip.MustParseAddr("198.51.100.1"), "v4", guardThreshold, guardT0)
	if _, locked := g.Locked(netip.MustParseAddr("198.51.100.2"), guardT0); locked {
		t.Fatal("IPv4 按单个地址计数，相邻地址不应受影响")
	}
}

// 记录满了时淘汰最久没有错误口令的地址，内存不会无限增长。
func TestAdminGuardCapacity(t *testing.T) {
	g := newAdminGuard()
	g.capacity = 3
	for i := 1; i <= 5; i++ {
		g.Fail(netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i)), "x", guardT0.Add(time.Duration(i)*time.Second))
	}
	if len(g.records) != 3 {
		t.Fatalf("记录数应封顶 3，实际 %d", len(g.records))
	}
	for _, gone := range []string{"192.0.2.1", "192.0.2.2"} {
		if g.records[guardKey(netip.MustParseAddr(gone))] != nil {
			t.Fatalf("%s 最久没有错误口令，应被淘汰", gone)
		}
	}
}

// 管理端鉴权：同一个错误口令重复不锁；第 10 个不同的错误口令返回 429；锁定中口令对了也是 429；
// 别的地址、没带口令的请求、网关接口不受影响。客户端地址用可信反代的 X-Forwarded-For 模拟。
func TestAdminAuthLockout(t *testing.T) {
	svc, _ := newTestServiceWith(t, mockupstream.Handler(), func(c *config.Config) {
		c.AllowNoAdminAuth = false
		c.AdminPassword = "secret"
		c.TrustedProxies = []string{"127.0.0.1"}
	})
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	from := func(ip, pw string) map[string]string {
		h := map[string]string{"X-Forwarded-For": ip}
		if pw != "" {
			h["X-Admin-Password"] = pw
		}
		return h
	}
	const attacker, admin, anon = "203.0.113.7", "198.51.100.9", "192.0.2.50"

	for i := 0; i < 30; i++ {
		if resp := doReq(t, srv, http.MethodGet, "/api/admin/session", from(attacker, "old-password"), ""); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("重复同一个错误口令第 %d 次应为 401，实际 %d", i+1, resp.StatusCode)
		}
	}
	for i := 1; i < guardThreshold; i++ {
		resp := doReq(t, srv, http.MethodGet, "/api/admin/session", from(attacker, fmt.Sprintf("guess-%d", i)), "")
		if i < guardThreshold-1 && resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("第 %d 个不同的错误口令应为 401，实际 %d", i+1, resp.StatusCode)
		}
		if i == guardThreshold-1 {
			if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "900" {
				t.Fatalf("第 %d 个不同的错误口令应锁定：429 且 Retry-After 900，实际 %d %q", guardThreshold, resp.StatusCode, resp.Header.Get("Retry-After"))
			}
			var body struct{ Code string }
			decode(t, resp, &body)
			if body.Code != "admin_locked" {
				t.Fatalf("锁定的错误体应带 code admin_locked，实际 %q", body.Code)
			}
		}
	}
	if resp := doReq(t, srv, http.MethodGet, "/api/admin/session", from(attacker, "secret"), ""); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("锁定中口令对了也应 429，实际 %d", resp.StatusCode)
	}
	if resp := doReq(t, srv, http.MethodGet, "/api/admin/session", from(admin, "secret"), ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("别的地址口令对时应 200，实际 %d", resp.StatusCode)
	}
	for i := 0; i < guardThreshold+5; i++ {
		if resp := doReq(t, srv, http.MethodGet, "/api/admin/session", from(anon, ""), ""); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("没带口令不算猜测，第 %d 次应仍为 401，实际 %d", i+1, resp.StatusCode)
		}
	}
	resp := doReq(t, srv, http.MethodGet, "/v1beta/models", from(attacker, ""), "")
	if resp.StatusCode == http.StatusTooManyRequests || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		t.Fatalf("网关接口不受管理端锁定影响，实际 %d", resp.StatusCode)
	}
}
