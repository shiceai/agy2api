package relay

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/logx"
)

// 没拿到上游响应的失败也记一条用量：状态码是返回给客户端的，账号是最后尝试的那个，详情里有原因。
func TestNoResponseFailuresRecorded(t *testing.T) {
	e := newEnv(t, "a")
	e.setCatalog("a", "m1")
	body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}}
	gen := func(ctx context.Context, id, model string) {
		t.Helper()
		req := Request{Model: model, Body: body, APIKeyID: 7, ClientIP: "10.0.0.1", UserAgent: "cli"}
		if _, err := e.relay.Generate(logx.WithRequestID(ctx, id), req); err == nil {
			t.Fatalf("%s: want error", id)
		}
	}

	// 模型不存在
	gen(context.Background(), "r-model", "nope")
	// 客户端在拿到结果前取消
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	gen(canceled, "r-canceled", "m1")
	// 唯一的账号连不上上游
	e.up.script["a"] = []int{dropConn}
	gen(context.Background(), "r-transport", "m1")
	// 唯一的账号在冷却：没有可用账号
	e.relay.pool.Cooldown(context.Background(), e.ids["a"], time.Now().Add(time.Minute), "rate_limit", "m1")
	gen(context.Background(), "r-busy", "m1")

	cases := []struct {
		id      string
		status  int
		kind    string
		account int64
	}{
		{"r-model", http.StatusNotFound, KindModelNotFound, 0},
		{"r-canceled", StatusClientClosed, KindCanceled, -1}, // 取消发生在选号前还是调用上游时都可能，账号不检查
		{"r-transport", http.StatusServiceUnavailable, KindTransport, e.ids["a"]},
		{"r-busy", http.StatusTooManyRequests, KindNoAccount, 0},
	}
	generic := errUnavailable(nil).Message // 回给客户端的通用说明，不该记进用量
	for _, c := range cases {
		rec, d := e.usageByRequest(c.id)
		if rec.StatusCode != c.status || d.Error == nil || d.Error.Kind != c.kind || rec.APIKeyID != 7 || rec.ClientIP != "10.0.0.1" ||
			(c.account >= 0 && rec.AccountID != c.account) {
			t.Errorf("%s: status %d account %d detail %+v", c.id, rec.StatusCode, rec.AccountID, d.Error)
		}
		if d.Error != nil && (d.Error.Message == "" || d.Error.Message == generic) {
			t.Errorf("%s: 失败说明应是真正的原因，实际 %q", c.id, d.Error.Message)
		}
	}
}

// 客户端在上游调用途中断开：不换号（也不替别的账号刷新令牌），不暂停账号，只记一次尝试，用量记 499。
func TestClientCancelMidCallStopsRetry(t *testing.T) {
	e := newEnv(t, "a", "b", "c")
	e.up.script["a"] = []int{stallStream} // 发一帧后挂住，非流式读响应体会一直等到请求取消
	ctx, cancel := context.WithCancel(logx.WithRequestID(context.Background(), "r-cancel-mid"))
	time.AfterFunc(200*time.Millisecond, cancel)
	body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}}
	_, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: body})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回取消错误，实际 %v", err)
	}
	for _, rt := range []string{"b", "c"} {
		if token, gen := e.up.calls(rt); token != 0 || gen != 0 {
			t.Fatalf("取消后不应再用账号 %s：令牌 %d 次、生成 %d 次", rt, token, gen)
		}
	}
	if paused := e.relay.Paused(); len(paused) != 0 {
		t.Fatalf("客户端取消不应暂停账号：%v", paused)
	}
	rec, d := e.usageByRequest("r-cancel-mid")
	if rec.StatusCode != StatusClientClosed || d.Error == nil || d.Error.Kind != KindCanceled || rec.AccountID != e.ids["a"] {
		t.Fatalf("用量应记 499 取消、账号 a：status %d account %d error %+v", rec.StatusCode, rec.AccountID, d.Error)
	}
	if len(d.Attempts) != 1 || d.Attempts[0].Result != KindCanceled {
		t.Fatalf("应只有一次尝试且结果为取消：%+v", d.Attempts)
	}
}

// 上游非流式响应超过大小上限：生成已经完成，不换号、不冷却账号，返回 502 提示改用流式，用量记 response_too_large。
func TestResponseTooLargeNotRetried(t *testing.T) {
	e := newEnv(t, "a", "b")
	e.up.script["a"] = []int{hugeBody}
	ctx := logx.WithRequestID(context.Background(), "r-too-large")
	body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}}
	_, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: body})
	var re *Error
	if !errors.As(err, &re) || re.Status != http.StatusBadGateway || !errors.Is(err, antigravity.ErrResponseTooLarge) {
		t.Fatalf("应返回 502 响应过大，实际 %v", err)
	}
	if _, gen := e.up.calls("b"); gen != 0 {
		t.Fatalf("响应过大不应换号，账号 b 被调用 %d 次", gen)
	}
	if cds := e.relay.Cooldowns(); len(cds) != 0 {
		t.Fatalf("响应过大不应冷却账号：%v", cds)
	}
	rec, d := e.usageByRequest("r-too-large")
	if rec.StatusCode != http.StatusBadGateway || d.Error == nil || d.Error.Kind != KindResponseTooLarge || rec.AccountID != e.ids["a"] {
		t.Fatalf("用量应记 502 响应过大、账号 a：status %d account %d error %+v", rec.StatusCode, rec.AccountID, d.Error)
	}
	if len(d.Attempts) != 1 || d.Attempts[0].Status != http.StatusOK || d.Attempts[0].Result != KindResponseTooLarge {
		t.Fatalf("应只有一次尝试，上游 200、结果为响应过大：%+v", d.Attempts)
	}
}
