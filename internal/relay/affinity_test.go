package relay

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/pool"
)

func withRequestID(id string) context.Context {
	return logx.WithRequestID(context.Background(), id)
}

// stickyEnv 是开了会话粘性的测试环境。
func stickyEnv(t *testing.T, accounts ...string) *env {
	t.Helper()
	e := newEnv(t, accounts...)
	p := e.relay.Policy()
	p.Sticky, p.StickyTTL = true, time.Hour
	e.relay.SetPolicy(p)
	return e
}

// turn 构造一个对话的第 n 轮请求体：首条用户消息相同，历史随轮次增长。
func turn(first string, n int) map[string]any {
	contents := []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": first}}}}
	for i := 1; i < n; i++ {
		contents = append(contents,
			map[string]any{"role": "model", "parts": []any{map[string]any{"text": fmt.Sprintf("reply %d", i)}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": fmt.Sprintf("more %d", i)}}})
	}
	return map[string]any{"contents": contents}
}

// 同一对话的多轮粘在同一个账号上；不同对话照常轮询分散。
func TestStickyConversation(t *testing.T) {
	e := stickyEnv(t, "a", "b")
	e.setCatalog("a", "m1")
	e.setCatalog("b", "m1")
	served := map[string]bool{}
	for c := range 4 {
		var first string
		for n := 1; n <= 3; n++ {
			status, _, rt, err := e.generateBody("m1", false, turn(fmt.Sprintf("conversation %d", c), n))
			if err != nil || status != http.StatusOK {
				t.Fatalf("conversation %d turn %d: %d %v", c, n, status, err)
			}
			if n == 1 {
				first = rt
			} else if rt != first {
				t.Fatalf("conversation %d turn %d moved from %s to %s", c, n, first, rt)
			}
		}
		served[first] = true
	}
	if len(served) != 2 {
		t.Fatalf("different conversations should spread over accounts: %v", served)
	}
}

// 上次的账号这次失败（限流）时换号重试，之后粘到新账号；用量详情记下换号和原因。
func TestStickyMovesAfterFailure(t *testing.T) {
	e := stickyEnv(t, "a", "b")
	e.setCatalog("a", "m1")
	e.setCatalog("b", "m1")
	_, _, owner, err := e.generateBody("m1", false, turn("hello", 1))
	if err != nil {
		t.Fatal(err)
	}
	other := map[string]string{"a": "b", "b": "a"}[owner]
	e.up.script[owner] = []int{http.StatusServiceUnavailable}
	_, _, rt, err := e.generateBody("m1", false, turn("hello", 2))
	if err != nil || rt != other {
		t.Fatalf("after failure should move to %s, got %s %v", other, rt, err)
	}
	if _, _, rt, _ := e.generateBody("m1", false, turn("hello", 3)); rt != other {
		t.Fatalf("should stick to the new account %s, got %s", other, rt)
	}
}

// 用量详情：新对话、沿用上次的账号、换号（原账号冷却）。
func TestStickyUsageDetail(t *testing.T) {
	e := stickyEnv(t, "a", "b")
	e.setCatalog("a", "m1")
	e.setCatalog("b", "m1")
	gen := func(id string, n int) {
		t.Helper()
		resp, err := e.relay.Generate(withRequestID(id), Request{Model: "m1", Body: turn("detail", n)})
		if err != nil {
			t.Fatal(err)
		}
		resp.Close()
	}
	gen("r-new", 1)
	gen("r-hit", 2)
	owner := ""
	for rt, id := range e.ids {
		if e.relay.affinity.get(affinityKey(Request{Model: "m1", Body: turn("detail", 1)}), time.Now(), time.Hour) == id {
			owner = rt
		}
	}
	e.relay.pool.Cooldown(withRequestID("x"), e.ids[owner], time.Now().Add(time.Minute), "rate_limit", "m1")
	gen("r-switch", 3)

	if _, d := e.usageByRequest("r-new"); d.Affinity == nil || d.Affinity.Result != AffinityNew {
		t.Fatalf("new: %+v", d.Affinity)
	}
	if _, d := e.usageByRequest("r-hit"); d.Affinity == nil || d.Affinity.Result != AffinityHit {
		t.Fatalf("hit: %+v", d.Affinity)
	}
	_, d := e.usageByRequest("r-switch")
	if d.Affinity == nil || d.Affinity.Result != AffinitySwitched || d.Affinity.Reason != pool.AffinityCooldown ||
		d.Affinity.FromAccountID != e.ids[owner] || d.Affinity.FromAccount == "" {
		t.Fatalf("switched: %+v", d.Affinity)
	}
}

// 记录闲置超过失效时间就不再粘；满了先清过期的，再清最久没用的。
func TestAffinityStore(t *testing.T) {
	s := newAffinityStore(10)
	now := time.Now()
	s.set("k", 7, now, time.Hour)
	if got := s.get("k", now.Add(30*time.Minute), time.Hour); got != 7 {
		t.Fatalf("within ttl: %d", got)
	}
	if got := s.get("k", now.Add(2*time.Hour), time.Hour); got != 0 {
		t.Fatalf("expired: %d", got)
	}
	for i := range 10 {
		s.set(fmt.Sprintf("k%d", i), int64(i), now.Add(time.Duration(i)*time.Second), time.Hour)
	}
	s.set("new", 99, now.Add(time.Minute), time.Hour)
	if len(s.entries) > 10 || s.get("k0", now.Add(time.Minute), time.Hour) != 0 || s.get("new", now.Add(time.Minute), time.Hour) != 99 {
		t.Fatalf("eviction: %d entries", len(s.entries))
	}
	if affinityKey(Request{Model: "m", Body: map[string]any{}}) != "" {
		t.Fatal("request without user message should not be sticky")
	}
}
