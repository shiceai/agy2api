package relay

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/logx"
)

// withCooldownWait 打开等冷却，5xx 休息时间设成 serverErr。
func (e *env) withCooldownWait(wait, serverErr time.Duration) {
	p := e.relay.Policy()
	p.CooldownWait = wait
	p.ServerErrorCooldown = serverErr
	e.relay.SetPolicy(p)
}

// 唯一的账号 5xx 后休息 300ms：在等待时间内，先等它恢复再试，客户端拿到成功结果；用量详情记下等了多久。
func TestCooldownWaitRetriesAfterShortCooldown(t *testing.T) {
	e := newEnv(t, "a")
	e.withCooldownWait(2*time.Second, 300*time.Millisecond)
	e.up.script["a"] = []int{500}

	start := time.Now()
	resp, err := e.relay.Generate(logx.WithRequestID(context.Background(), "r-wait"), Request{Model: "m1", Body: map[string]any{}})
	if err != nil {
		t.Fatalf("等冷却后应成功，实际 %v", err)
	}
	resp.Close()
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("应等约 300ms 再试，实际 %v", elapsed)
	}
	if _, gen := e.up.calls("a"); gen != 2 {
		t.Fatalf("a 应被调用 2 次（失败一次、等完再试一次），实际 %d", gen)
	}
	rec, d := e.usageByRequest("r-wait")
	if rec.StatusCode != http.StatusOK || len(d.Attempts) != 2 || d.Attempts[0].Result != "server_error" ||
		d.Timings == nil || d.Timings.CooldownWaitMs == nil || *d.Timings.CooldownWaitMs < 250 {
		t.Fatalf("用量应记成功、两次尝试和等冷却的时长：status %d detail %+v timings %+v", rec.StatusCode, d, d.Timings)
	}
}

// 最早恢复的时间超过等待时间：不等，立即返回。
func TestCooldownWaitSkipsLongCooldown(t *testing.T) {
	e := newEnv(t, "a")
	e.withCooldownWait(time.Second, 3*time.Second)
	e.up.script["a"] = []int{500}

	start := time.Now()
	_, _, _, err := e.generate("m1", false)
	if re := relayError(t, err); re.Status != http.StatusServiceUnavailable {
		t.Fatalf("应返回 503，实际 %d %v", re.Status, err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("冷却比等待时间长，不应等，实际用了 %v", elapsed)
	}
}

// 之前的请求留下的冷却：一开始就选不到账号，最早的很快恢复，等到那时再选，不先返回 429。
func TestCooldownWaitForExistingCooldown(t *testing.T) {
	e := newEnv(t, "a", "b")
	e.withCooldownWait(2*time.Second, 0)
	ctx := context.Background()
	e.relay.pool.Cooldown(ctx, e.ids["a"], time.Now().Add(300*time.Millisecond), "test", "m1")
	e.relay.pool.Cooldown(ctx, e.ids["b"], time.Now().Add(time.Minute), "test", "m1")

	start := time.Now()
	status, _, rt, err := e.generate("m1", false)
	if err != nil || status != http.StatusOK || rt != "a" {
		t.Fatalf("应等 a 恢复后由 a 返回 200，实际 %s %d %v", rt, status, err)
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("应等到 a 冷却结束，实际只用了 %v", elapsed)
	}
}

// 等待期间客户端取消：立即结束，按取消处理。
func TestCooldownWaitCanceled(t *testing.T) {
	e := newEnv(t, "a")
	e.withCooldownWait(10*time.Second, 0)
	e.relay.pool.Cooldown(context.Background(), e.ids["a"], time.Now().Add(5*time.Second), "test", "m1")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := e.relay.Generate(ctx, Request{Model: "m1", Body: map[string]any{}})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("取消后应立即结束，实际 %v（用了 %v）", err, time.Since(start))
	}
}

// 等冷却之后的尝试仍算在"最多尝试"里：一直失败时最多打上游 MaxAttempts 次。
func TestCooldownWaitBoundedByMaxAttempts(t *testing.T) {
	e := newEnv(t, "a")
	e.withCooldownWait(5*time.Second, 100*time.Millisecond)
	e.up.script["a"] = []int{500, 500, 500, 500, 500}

	if _, _, _, err := e.generate("m1", false); err == nil {
		t.Fatal("一直 5xx 应失败")
	}
	if _, gen := e.up.calls("a"); gen != e.relay.Policy().MaxAttempts {
		t.Fatalf("应只尝试 %d 次，实际 %d", e.relay.Policy().MaxAttempts, gen)
	}
}
