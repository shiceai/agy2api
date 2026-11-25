package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/logx"
)

// 流式中途上游不再发数据：超过空闲超时就断开，读响应体得到 ErrStreamIdle，已收到的内容保留；
// 用量记成失败（504，原因 stream_idle）。
func TestStreamIdleTimeout(t *testing.T) {
	e := newEnv(t, "a")
	e.setCatalog("a", "m1")
	p := e.relay.Policy()
	p.StreamIdleTimeout = 200 * time.Millisecond
	e.relay.SetPolicy(p)
	e.up.script["a"] = []int{stallStream}

	body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}}
	start := time.Now()
	resp, err := e.relay.Generate(logx.WithRequestID(context.Background(), "r-idle"), Request{Model: "m1", Body: body, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Close()
	elapsed := time.Since(start)
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("want ErrStreamIdle, got %v", err)
	}
	if !strings.Contains(string(got), "from a") {
		t.Fatalf("frame before stall lost: %q", got)
	}
	if elapsed < 200*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("idle cut after %s", elapsed)
	}

	rec, d := e.usageByRequest("r-idle")
	if rec.StatusCode != http.StatusGatewayTimeout || d.Error == nil || d.Error.Kind != KindStreamIdle ||
		len(d.Attempts) != 1 || d.Attempts[0].Result != KindStreamIdle {
		t.Fatalf("usage: status %d detail %+v", rec.StatusCode, d)
	}
}

// 请求用开始时的配置快照：中途改了空闲超时，正在进行的流仍按原来的时长断开，用量里记的也是原来的时长。
func TestStreamUsesConfigSnapshot(t *testing.T) {
	e := newEnv(t, "a")
	e.setCatalog("a", "m1")
	p := e.relay.Policy()
	p.StreamIdleTimeout = 200 * time.Millisecond
	e.relay.SetPolicy(p)
	e.up.script["a"] = []int{stallStream}

	body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}}
	resp, err := e.relay.Generate(logx.WithRequestID(context.Background(), "r-snapshot"), Request{Model: "m1", Body: body, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	p.StreamIdleTimeout = 10 * time.Minute
	e.relay.SetPolicy(p)
	if _, err := io.ReadAll(resp.Body); !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("进行中的流应按原来的 200ms 断开，实际 %v", err)
	}
	resp.Close()

	_, d := e.usageByRequest("r-snapshot")
	if d.Error == nil || !strings.Contains(d.Error.Message, "200ms") {
		t.Fatalf("用量里应记实际用的 200ms：%+v", d.Error)
	}
}

// 空闲超时关闭（0）时不检查，正常的流不受影响。
func TestStreamIdleDisabled(t *testing.T) {
	e := newEnv(t, "a")
	e.setCatalog("a", "m1")
	p := e.relay.Policy()
	p.StreamIdleTimeout = 0
	e.relay.SetPolicy(p)
	status, body, _, err := e.generate("m1", true)
	if err != nil || status != http.StatusOK || !strings.Contains(body, "from a") {
		t.Fatalf("stream: %d %q %v", status, body, err)
	}
}

// 上游在流式中途推来错误帧：之前的内容照常给客户端，错误帧不转发，读响应体得到它，改写成 429 错误事件；
// 账号和非 2xx 一样冷却（额度耗尽按上游给的 90 秒），用量记成失败（429、quota_exhausted），下一个请求换号。
func TestMidStreamErrorFrame(t *testing.T) {
	e := newEnv(t, "a", "b")
	e.up.script["a"] = []int{midStream429}
	body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}}
	resp, err := e.relay.Generate(logx.WithRequestID(context.Background(), "r-mid"), Request{Model: "m1", Body: body, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if served := e.account(resp.call.lease.Account.ID); served != "a" {
		t.Fatalf("应由 a 开始输出，实际 %s", served)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Close()
	if !strings.Contains(string(got), "from a") || strings.Contains(string(got), "RESOURCE_EXHAUSTED") {
		t.Fatalf("错误帧之前的内容应照常输出、错误帧不转发：%q", got)
	}
	if re := StreamFailure(err); re == nil || re.Status != http.StatusTooManyRequests || re.Code != "RESOURCE_EXHAUSTED" {
		t.Fatalf("错误帧应改写成 429 错误事件，实际 %v → %+v", err, re)
	}
	cds := e.relay.Cooldowns()[e.ids["a"]]
	if len(cds) != 1 || cds[0].Model != "m1" || time.Until(cds[0].Until) < 80*time.Second {
		t.Fatalf("a 的 m1 应按上游时间冷却约 90 秒，实际 %+v", cds)
	}
	if _, _, rt, err := e.generate("m1", true); err != nil || rt != "b" {
		t.Fatalf("a 冷却中，下一个请求应换到 b，实际 %s %v", rt, err)
	}

	rec, d := e.usageByRequest("r-mid")
	if rec.StatusCode != http.StatusTooManyRequests || d.Error == nil || d.Error.Kind != "quota_exhausted" ||
		len(d.Attempts) != 1 || d.Attempts[0].Status != http.StatusTooManyRequests || d.Attempts[0].CooldownMs < 80_000 {
		t.Fatalf("用量应记成 429 失败并带冷却：status %d detail %+v", rec.StatusCode, d)
	}
}

// 流式中途出错时给客户端的错误事件：空闲断开 504，错误帧按分类改写，正常结束和其它错误不发。
func TestStreamFailure(t *testing.T) {
	cases := []struct {
		err    error
		status int
	}{
		{ErrStreamIdle, http.StatusGatewayTimeout},
		{&antigravity.StreamError{UpstreamError: antigravity.UpstreamError{Status: 429, Kind: antigravity.KindRateLimit}}, http.StatusTooManyRequests},
		{&antigravity.StreamError{UpstreamError: antigravity.UpstreamError{Status: 500, Kind: antigravity.KindServerError, Message: "internal detail"}}, http.StatusServiceUnavailable},
		{&antigravity.StreamError{UpstreamError: antigravity.UpstreamError{Status: 400, Kind: antigravity.KindClientError, Message: "bad part"}}, http.StatusBadRequest},
		{io.EOF, 0},
		{context.Canceled, 0},
	}
	for _, tc := range cases {
		re := StreamFailure(tc.err)
		if (re == nil) != (tc.status == 0) || (re != nil && re.Status != tc.status) {
			t.Fatalf("%v → %+v，期望 %d", tc.err, re, tc.status)
		}
		if re != nil && tc.status >= 500 && strings.Contains(re.Message, "internal detail") {
			t.Fatalf("5xx 不应把上游原文发给客户端：%s", re.Message)
		}
	}
}

// 流式在第一帧发给客户端之前出错（错误帧、连接断开、空闲超时）：客户端什么都还没收到，换号重试，
// 拿到的是 b 的完整结果；用量记成功，详情里第一次尝试记失败原因，错误帧和非 2xx 一样冷却。
func TestStreamRetriesBeforeFirstFrame(t *testing.T) {
	cases := []struct {
		name     string
		script   int
		result   string
		cooldown bool
	}{
		{"错误帧", inBand503, string(antigravity.KindServerError), true},
		{"连接断开", dropBeforeFrame, KindTransport, false},
		{"空闲超时", stallBeforeFrame, KindStreamIdle, false},
	}
	body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "a", "b")
			p := e.relay.Policy()
			p.StreamIdleTimeout = 200 * time.Millisecond
			e.relay.SetPolicy(p)
			e.up.script["a"] = []int{tc.script}

			resp, err := e.relay.Generate(logx.WithRequestID(context.Background(), "r-first"), Request{Model: "m1", Body: body, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(resp.Body)
			resp.Close()
			if served := e.account(resp.call.lease.Account.ID); err != nil || served != "b" || !strings.Contains(string(got), "from b") || strings.Contains(string(got), "error") {
				t.Fatalf("应透明地换到 b 并拿到完整结果，实际 %s %v %q", served, err, got)
			}
			if cds := e.relay.Cooldowns()[e.ids["a"]]; (len(cds) > 0) != tc.cooldown {
				t.Fatalf("a 的冷却不对（期望 %v）：%+v", tc.cooldown, cds)
			}
			rec, d := e.usageByRequest("r-first")
			if rec.StatusCode != http.StatusOK || len(d.Attempts) != 2 || d.Attempts[0].Result != tc.result || d.Attempts[1].Result != "success" {
				t.Fatalf("用量应记成功、第一次尝试记 %s：status %d attempts %+v", tc.result, rec.StatusCode, d.Attempts)
			}
		})
	}
}

// 只有一个账号、第一帧就是错误帧：换号次数用完，客户端直接收到错误（503），而不是 200 加错误事件；
// 用量记错误帧的状态码（上游 HTTP 状态是 200）。
func TestStreamErrorFrameFirstExhausted(t *testing.T) {
	e := newEnv(t, "a")
	e.up.script["a"] = []int{inBand503}
	body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}}
	_, err := e.relay.Generate(logx.WithRequestID(context.Background(), "r-only"), Request{Model: "m1", Body: body, Stream: true})
	if re := relayError(t, err); re.Status != http.StatusServiceUnavailable {
		t.Fatalf("应直接返回 503，实际 %d %v", re.Status, err)
	}
	rec, d := e.usageByRequest("r-only")
	if rec.StatusCode != http.StatusServiceUnavailable || d.Error == nil || d.Error.Kind != string(antigravity.KindServerError) {
		t.Fatalf("用量应记 503 server_error：status %d detail %+v", rec.StatusCode, d)
	}
}
