package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/store"
)

// usageByRequest 关闭执行层（写完排队的用量）后按请求 ID 取记录和解析好的详情。
func (e *env) usageByRequest(reqID string) (*store.UsageRecord, store.UsageDetail) {
	e.t.Helper()
	if err := e.relay.Close(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	records, err := e.store.RecentUsage(context.Background(), 20)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, r := range records {
		if r.RequestID == reqID {
			var d store.UsageDetail
			if err := json.Unmarshal(r.Detail, &d); err != nil {
				e.t.Fatalf("详情不是合法 JSON：%v %s", err, r.Detail)
			}
			return r, d
		}
	}
	e.t.Fatalf("没有请求 %s 的用量记录", reqID)
	return nil, store.UsageDetail{}
}

// 成功的流式请求：记客户端 IP 和 UA；详情里有选号、取令牌、响应头、首个上游帧、首个正文的时间点，
// 并发快照，上游响应 ID 和 trace ID，尝试列表里只有一次成功。
func TestUsageDetailOnSuccess(t *testing.T) {
	e := newEnv(t, "a")
	ctx := logx.WithRequestID(context.Background(), "req-ok")
	resp, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: map[string]any{}, Stream: true,
		ClientIP: "203.0.113.7", UserAgent: "test-client/1.0 (x)"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Close()

	rec, d := e.usageByRequest("req-ok")
	if rec.ClientIP != "203.0.113.7" || rec.UserAgent != "test-client/1.0 (x)" {
		t.Fatalf("应记客户端 IP 和 UA：%q %q", rec.ClientIP, rec.UserAgent)
	}
	tm := d.Timings
	if tm == nil || tm.AcquireMs == nil || tm.TokenMs == nil || tm.HeaderMs == nil || tm.FirstEventMs == nil || tm.FirstTextMs == nil {
		t.Fatalf("流式应有全部时间点：%+v", tm)
	}
	if *tm.HeaderMs > *tm.FirstEventMs || *tm.FirstEventMs > *tm.FirstTextMs || *tm.FirstTextMs > rec.LatencyMs {
		t.Fatalf("时间点应依次递增：header %d event %d text %d total %d", *tm.HeaderMs, *tm.FirstEventMs, *tm.FirstTextMs, rec.LatencyMs)
	}
	if tm.UpstreamMs == nil || *tm.UpstreamMs != 42 {
		t.Fatalf("应记上游 Server-Timing 的处理时长 42ms：%v", tm.UpstreamMs)
	}
	if d.Slot == nil || d.Slot.InUse != 1 || d.Slot.Limit != DefaultPolicy().Concurrency {
		t.Fatalf("并发快照应为 1 / 默认上限：%+v", d.Slot)
	}
	if d.ResponseID != "resp-a" || d.TraceID != "trace-a" {
		t.Fatalf("应记上游响应 ID 和 trace ID：%q %q", d.ResponseID, d.TraceID)
	}
	if len(d.Attempts) != 1 || d.Attempts[0].Result != "success" || d.Attempts[0].Account != "a@example.com" || d.Error != nil {
		t.Fatalf("尝试列表应只有一次成功：%+v %+v", d.Attempts, d.Error)
	}
}

// 换号成功：尝试列表依次是第一个号的限流（带冷却）和第二个号的成功。
func TestUsageDetailRecordsAttempts(t *testing.T) {
	e := newEnv(t, "a", "b")
	e.up.script["a"] = []int{429}
	ctx := logx.WithRequestID(context.Background(), "req-retry")
	resp, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Close()

	_, d := e.usageByRequest("req-retry")
	if len(d.Attempts) != 2 {
		t.Fatalf("应有两次尝试：%+v", d.Attempts)
	}
	first, second := d.Attempts[0], d.Attempts[1]
	if first.Try != 1 || first.Account != "a@example.com" || first.Status != http.StatusTooManyRequests ||
		first.Result != "quota_exhausted" || first.CooldownMs < 85_000 {
		t.Fatalf("第一次应是 a 的额度耗尽并冷却约 90 秒：%+v", first)
	}
	if second.Try != 2 || second.Account != "b@example.com" || second.Result != "success" {
		t.Fatalf("第二次应是 b 成功：%+v", second)
	}
	if d.TraceID != "trace-b" {
		t.Fatalf("trace ID 应取成功那次的：%q", d.TraceID)
	}
}

// 最终失败：详情里有失败原因和上游响应头里的 trace ID，非流式没有首个上游帧。
func TestUsageDetailOnFailure(t *testing.T) {
	e := newEnv(t, "a")
	e.up.script["a"] = []int{400}
	ctx := logx.WithRequestID(context.Background(), "req-bad")
	if _, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: map[string]any{}}); err == nil {
		t.Fatal("应失败")
	}
	rec, d := e.usageByRequest("req-bad")
	if rec.StatusCode != 400 || d.Error == nil || d.Error.Kind != "client_error" || d.Error.Message != "scripted" {
		t.Fatalf("应记失败原因：%d %+v", rec.StatusCode, d.Error)
	}
	if d.TraceID != "err-trace-a" || d.Timings.HeaderMs == nil || d.Timings.FirstEventMs != nil {
		t.Fatalf("应有响应头里的 trace ID 和响应头时间，没有首个上游帧：%q %+v", d.TraceID, d.Timings)
	}
	if len(d.Attempts) != 1 || d.Attempts[0].Result != "client_error" || d.Attempts[0].Status != 400 {
		t.Fatalf("尝试列表应记这次失败：%+v", d.Attempts)
	}
}
