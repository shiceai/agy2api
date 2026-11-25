package antigravity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 提前量按账号稳定散开在 [MinLead, MaxLead]；关闭提前刷新时为 0。
func TestRefreshPolicyLead(t *testing.T) {
	p := DefaultRefreshPolicy()
	seen := map[time.Duration]bool{}
	for id := int64(1); id <= 50; id++ {
		l := p.Lead(id)
		if l < p.MinLead || l > p.MaxLead || l%time.Second != 0 {
			t.Fatalf("账号 %d 的提前量 %v 不在范围内或不是整秒", id, l)
		}
		if l != p.Lead(id) {
			t.Fatalf("同一账号的提前量应稳定")
		}
		seen[l] = true
	}
	if len(seen) < 10 {
		t.Fatalf("50 个账号的提前量应散开，实际只有 %d 种", len(seen))
	}
	if l := (RefreshPolicy{Early: false, MinLead: time.Minute, MaxLead: time.Hour}).Lead(1); l != 0 {
		t.Fatalf("关闭提前刷新时提前量应为 0，实际 %v", l)
	}
	if l := (RefreshPolicy{Early: true, MinLead: time.Minute, MaxLead: time.Minute}).Lead(1); l != time.Minute {
		t.Fatalf("上下限相同时提前量应为该值，实际 %v", l)
	}
}

// tokenServer 是可编程的令牌端点：expiresIn 决定签发令牌的有效期，fail 为真时返回 invalid_grant。
type tokenServer struct {
	calls     atomic.Int32
	expiresIn atomic.Int64
	fail      atomic.Bool
	delay     time.Duration
}

func (s *tokenServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := s.calls.Add(1)
	time.Sleep(s.delay)
	w.Header().Set("Content-Type", "application/json")
	if s.fail.Load() {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	fmt.Fprintf(w, `{"access_token":"at-%d","expires_in":%d}`, n, s.expiresIn.Load())
}

func newTokenEnv(t *testing.T, delay time.Duration) (*TokenManager, *tokenServer) {
	t.Helper()
	ts := &tokenServer{delay: delay}
	ts.expiresIn.Store(3600)
	srv := httptest.NewServer(ts)
	t.Cleanup(srv.Close)
	return NewTokenManager(NewClient(Options{TokenURL: srv.URL}), nil), ts
}

// 同一账号的并发请求只刷新一次，共享结果；失败也共享，且短时间内不重刷。
func TestAccessTokenMergesConcurrentRefresh(t *testing.T) {
	m, ts := newTokenEnv(t, 50*time.Millisecond)
	var wg sync.WaitGroup
	tokens := make([]string, 8)
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], _ = m.AccessToken(context.Background(), 1, Credential{RefreshToken: "rt"})
		}()
	}
	wg.Wait()
	if ts.calls.Load() != 1 {
		t.Fatalf("并发请求应只刷新一次，实际 %d 次", ts.calls.Load())
	}
	for _, tok := range tokens {
		if tok != "at-1" {
			t.Fatalf("所有请求应拿到同一个令牌：%v", tokens)
		}
	}

	// 刷新失败：等待者共享错误，失败后短时间内不再打令牌端点。
	m2, ts2 := newTokenEnv(t, 50*time.Millisecond)
	ts2.fail.Store(true)
	var errs atomic.Int32
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m2.AccessToken(context.Background(), 1, Credential{RefreshToken: "rt"}); IsInvalidGrant(err) {
				errs.Add(1)
			}
		}()
	}
	wg.Wait()
	if _, err := m2.AccessToken(context.Background(), 1, Credential{RefreshToken: "rt"}); !IsInvalidGrant(err) {
		t.Fatalf("退避期内应直接返回上次的错误：%v", err)
	}
	if ts2.calls.Load() != 1 || errs.Load() != 5 {
		t.Fatalf("失败也应合并：刷新 %d 次，拿到 invalid_grant 的请求 %d 个", ts2.calls.Load(), errs.Load())
	}
}

// 进入提前刷新窗口就刷新；刷新失败但令牌没过期时继续用旧令牌。关闭提前刷新时只在过期后刷新。
func TestAccessTokenRefreshLead(t *testing.T) {
	m, ts := newTokenEnv(t, 0)
	m.SetPolicy(RefreshPolicy{Early: true, MinLead: 10 * time.Minute, MaxLead: 10 * time.Minute})
	ts.expiresIn.Store(int64((5 * time.Minute).Seconds())) // 签发即在 10 分钟窗口内
	cred := Credential{RefreshToken: "rt"}
	if tok, _ := m.AccessToken(context.Background(), 1, cred); tok != "at-1" {
		t.Fatalf("首次应刷新，实际 %q", tok)
	}
	if tok, _ := m.AccessToken(context.Background(), 1, cred); tok != "at-2" || ts.calls.Load() != 2 {
		t.Fatalf("离过期不到 10 分钟应提前刷新：%q，刷新 %d 次", tok, ts.calls.Load())
	}
	ts.fail.Store(true)
	if tok, err := m.AccessToken(context.Background(), 1, cred); tok != "at-2" || err != nil {
		t.Fatalf("刷新失败但令牌未过期时应沿用旧令牌：%q %v", tok, err)
	}

	m2, ts2 := newTokenEnv(t, 0)
	m2.SetPolicy(RefreshPolicy{Early: false})
	ts2.expiresIn.Store(int64((5 * time.Minute).Seconds()))
	for range 3 {
		if tok, _ := m2.AccessToken(context.Background(), 1, cred); tok != "at-1" {
			t.Fatalf("关闭提前刷新时未过期的令牌应一直用：%q", tok)
		}
	}
	if ts2.calls.Load() != 1 {
		t.Fatalf("关闭提前刷新时不应提前刷新，实际刷新 %d 次", ts2.calls.Load())
	}
	m2.Invalidate(1)
	if tok, _ := m2.AccessToken(context.Background(), 1, cred); tok != "at-2" {
		t.Fatalf("401 后清掉缓存应重新刷新：%q", tok)
	}
}
