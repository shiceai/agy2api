package relay

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/pool"
	"github.com/shiceai/agy2api/internal/store"
)

// trace 记录一次生成请求的过程：每次换号尝试的结果，最终写进用量记录的详情（store.UsageDetail）。
type trace struct {
	start      time.Time
	attempts   []store.UsageAttempt
	fakeStream bool
	affinity   *store.UsageAffinity
	// cooldownWait 是这次请求等冷却的累计时长（见 waitCooldown）。
	cooldownWait time.Duration
}

// cooldownWaitMs 返回等冷却的累计毫秒数，没等过时为 nil。
func (t *trace) cooldownWaitMs() *int64 {
	if t.cooldownWait <= 0 {
		return nil
	}
	return ms(t.cooldownWait)
}

// add 记一次尝试。started 是这次尝试开始选号的时间，耗时含选号等待。
func (t *trace) add(try int, lease *pool.Lease, started time.Time, status int, result, errMsg string, cooldown time.Duration) {
	t.attempts = append(t.attempts, store.UsageAttempt{
		Try:        try,
		AccountID:  lease.Account.ID,
		Account:    lease.Account.Email,
		Model:      lease.Model,
		Status:     status,
		Result:     result,
		Error:      errMsg,
		CooldownMs: cooldown.Milliseconds(),
		DurationMs: time.Since(started).Milliseconds(),
	})
}

// call 是最终那次尝试（成功，或决定了最终结果的失败）的信息。
type call struct {
	try        int
	started    time.Time
	lease      *pool.Lease
	up         *antigravity.Response
	acquireDur time.Duration
	tokenDur   time.Duration
}

// detail 生成详情 JSON。uerr 是最终失败原因，成功时为 nil；cost 是费用，没算时为 nil。流式要在读完后调用，时间点才完整。
func (t *trace) detail(c *call, uerr *store.UsageError, cost *store.UsageCost) json.RawMessage {
	d := store.UsageDetail{
		Timings: &store.UsageTimings{
			CooldownWaitMs: t.cooldownWaitMs(),
			AcquireMs:      ms(c.acquireDur),
			TokenMs:        ms(c.tokenDur),
		},
		Attempts:   t.attempts,
		Error:      uerr,
		FakeStream: t.fakeStream,
		Cost:       cost,
		Affinity:   t.affinity,
	}
	// 账号检测的租约不占并发名额（Limit 为 0），不记并发占用。
	if c.lease.Limit > 0 {
		d.Slot = &store.UsageSlot{InUse: c.lease.InUse, Limit: c.lease.Limit}
	}
	if c.up != nil {
		m := c.up.Marks()
		d.Timings.HeaderMs = offset(t.start, c.up.HeaderAt)
		d.Timings.FirstEventMs = offset(t.start, m.FirstEventAt)
		d.Timings.FirstTextMs = offset(t.start, m.FirstTextAt)
		d.ResponseID, d.TraceID = m.ResponseID, m.TraceID
		if dur, ok := c.up.ServerTiming(); ok {
			d.Timings.UpstreamMs = ms(dur)
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		slog.Error("用量详情序列化失败", "err", err)
		return nil
	}
	return raw
}

func ms(d time.Duration) *int64 {
	v := d.Milliseconds()
	return &v
}

// offset 返回 at 距 start 的毫秒数，at 为零值时返回 nil。
func offset(start, at time.Time) *int64 {
	if at.IsZero() {
		return nil
	}
	return ms(at.Sub(start))
}
