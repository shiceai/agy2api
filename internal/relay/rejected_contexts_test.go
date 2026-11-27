package relay

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/store"
)

// chatBody 是一段对话：texts 依次是 user、model、user……
func chatBody(texts ...string) map[string]any {
	contents := make([]any, len(texts))
	for i, t := range texts {
		role := "user"
		if i%2 == 1 {
			role = "model"
		}
		contents[i] = map[string]any{"role": role, "parts": []any{map[string]any{"text": t}}}
	}
	return map[string]any{"contents": contents}
}

func (e *env) calls() int {
	n := 0
	for _, name := range []string{"a", "b", "c"} {
		_, c := e.up.calls(name)
		n += c
	}
	return n
}

// 请求被拒后：原样重试、在后面继续对话都在本地返回 400，不再发上游；删改过的、新开的对话照常发往上游
// （a、b 被拒后冷却，由 c 接）。
func TestRejectedContextBlocksRetryAndContinuation(t *testing.T) {
	e := newEnv(t, "a", "b", "c")
	p := DefaultPolicy()
	p.RateLimitRetryDelay = 0
	e.relay.SetPolicy(p)
	for _, name := range []string{"a", "b", "c"} {
		e.up.script[name] = []int{transient429}
	}

	gen := func(reqID string, body map[string]any) (int, error) {
		resp, err := e.relay.Generate(logx.WithRequestID(context.Background(), reqID), Request{Model: "gemini-2.5-flash", Body: body, ClientIP: "203.0.113.5"})
		if err != nil {
			return relayError(t, err).Status, err
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Close()
		return resp.Status, nil
	}
	if status, _ := gen("req-first", chatBody("设定", "回复", "继续")); status != http.StatusTooManyRequests {
		t.Fatalf("第一次应被判为请求被拒（429），实际 %d", status)
	}
	// 被拒的两个号冷却，没试到的那个号之后正常回复。
	for _, name := range []string{"a", "b", "c"} {
		e.up.script[name] = nil
	}
	before := e.calls()

	for name, body := range map[string]map[string]any{
		"原样重试": chatBody("设定", "回复", "继续"),
		"继续对话": chatBody("设定", "回复", "继续", "（没有回复）", "再继续"),
	} {
		status, err := gen("req-"+name, body)
		if re := relayError(t, err); status != http.StatusBadRequest || re.Code != "FAILED_PRECONDITION" {
			t.Fatalf("%s 应在本地返回 400：%d %+v", name, status, re)
		}
	}
	if got := e.calls(); got != before {
		t.Fatalf("包含被拒上下文的请求不应发往上游：调用 %d → %d", before, got)
	}

	for name, body := range map[string]map[string]any{
		"删改最后一条": chatBody("设定", "回复", "换个说法"),
		"新开对话":   chatBody("你好"),
	} {
		if status, err := gen("req-"+name, body); err != nil || status != http.StatusOK {
			t.Fatalf("%s 应照常发往上游并成功：%d %v", name, status, err)
		}
	}

	_, d := e.usageByRequest("req-原样重试")
	if d.Error == nil || d.Error.Kind != KindContextRejected || len(d.Attempts) != 0 {
		t.Fatalf("用量应记为上下文已被拒、没有尝试：%+v %+v", d.Error, d.Attempts)
	}
	list, err := e.store.RejectedContexts(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("库里应有 1 条被拒上下文：%v %+v", err, list)
	}
	if c := list[0]; c.Turns != 3 || c.Hits != 2 || c.RequestID != "req-first" || c.ClientIP != "203.0.113.5" || c.Model != "gemini-2.5-flash" {
		t.Fatalf("被拒上下文记录不对：%+v", c)
	}
}

// 关闭拦截：被拒后不记指纹，重试照常发往上游（a、b 被拒后冷却，由 c 接）。
func TestRejectedContextDisabled(t *testing.T) {
	e := newEnv(t, "a", "b", "c")
	p := DefaultPolicy()
	p.RateLimitRetryDelay = 0
	e.relay.SetPolicy(p)
	e.relay.SetContextBlock(false)
	for _, name := range []string{"a", "b", "c"} {
		e.up.script[name] = []int{transient429}
	}
	if _, err := e.relay.Generate(context.Background(), Request{Model: "gemini-2.5-flash", Body: chatBody("设定")}); err == nil {
		t.Fatal("第一次应失败")
	}
	for _, name := range []string{"a", "b", "c"} {
		e.up.script[name] = nil
	}
	resp, err := e.relay.Generate(context.Background(), Request{Model: "gemini-2.5-flash", Body: chatBody("设定")})
	if err != nil {
		t.Fatalf("关闭后重试应照常发往上游：%v", err)
	}
	resp.Close()
}

// 启动时从库读回：有效的生效，超过 RejectedContextTTL 没再拦到的删掉、不生效。
func TestRejectedContextsLoad(t *testing.T) {
	e := newEnv(t, "a")
	fps := func(texts ...string) string {
		all := antigravity.ContextFingerprints(chatBody(texts...))
		return all[len(all)-1]
	}
	now := time.Now()
	ctx := context.Background()
	_ = e.store.AddRejectedContext(ctx, store.RejectedContext{Fingerprint: fps("有效"), Turns: 1, CreatedAt: now.UnixMilli()})
	_ = e.store.AddRejectedContext(ctx, store.RejectedContext{Fingerprint: fps("过期"), Turns: 1, CreatedAt: now.Add(-RejectedContextTTL - time.Hour).UnixMilli()})
	if err := e.relay.LoadRejectedContexts(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: chatBody("有效")}); relayError(t, err).Status != http.StatusBadRequest {
		t.Fatal("读回的被拒上下文应生效")
	}
	resp, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: chatBody("过期")})
	if err != nil {
		t.Fatalf("过期的不应生效：%v", err)
	}
	resp.Close()
	if list, _ := e.store.RejectedContexts(ctx); len(list) != 1 {
		t.Fatalf("过期的应从库里删掉：%+v", list)
	}
}
