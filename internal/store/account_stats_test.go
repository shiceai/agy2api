package store

import (
	"context"
	"encoding/json"
	"testing"
)

// 统计按尝试算：A 号 429 后换到 B 号成功，最终那条用量记在 B 号名下，A 号的失败仍算进 A 号。
func TestAccountAttemptStats(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	detail := func(attempts ...UsageAttempt) json.RawMessage {
		raw, _ := json.Marshal(UsageDetail{Attempts: attempts})
		return raw
	}
	recs := []*UsageRecord{
		// 1 号直接成功
		{RequestID: "r1", AccountID: 1, StatusCode: 200, CreatedAt: 1000, Detail: detail(
			UsageAttempt{Try: 1, AccountID: 1, Model: "flash", Result: "success", DurationMs: 100},
		)},
		// 1 号 429，换 2 号成功
		{RequestID: "r2", AccountID: 2, StatusCode: 200, CreatedAt: 2000, Detail: detail(
			UsageAttempt{Try: 1, AccountID: 1, Model: "flash", Status: 429, Result: "rate_limit", Error: "slow down", CooldownMs: 60000, DurationMs: 10},
			UsageAttempt{Try: 2, AccountID: 2, Model: "flash", Status: 200, Result: "success", DurationMs: 400},
		)},
		// 1 号在另一个模型上成功
		{RequestID: "r3", AccountID: 1, StatusCode: 200, CreatedAt: 3000, Detail: detail(
			UsageAttempt{Try: 1, AccountID: 1, Model: "claude", Result: "success", DurationMs: 300},
		)},
		// 老记录：没有尝试明细，不计入也不能报错
		{RequestID: "r4", AccountID: 1, StatusCode: 200, CreatedAt: 4000},
		// 时间范围之前
		{RequestID: "r0", AccountID: 1, StatusCode: 500, CreatedAt: 10, Detail: detail(
			UsageAttempt{Try: 1, AccountID: 1, Model: "flash", Status: 500, Result: "server_error", DurationMs: 5},
		)},
	}
	if err := s.InsertUsage(ctx, recs...); err != nil {
		t.Fatal(err)
	}

	stats, err := s.AccountModelStats(ctx, 1, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("stats: %+v", stats)
	}
	if f := stats[0]; f.Model != "flash" || f.Attempts != 2 || f.Success != 1 || f.AvgMs != 100 {
		t.Fatalf("flash: %+v", f)
	}
	if c := stats[1]; c.Model != "claude" || c.Attempts != 1 || c.Success != 1 || c.AvgMs != 300 {
		t.Fatalf("claude: %+v", c)
	}

	errs, err := s.AccountRecentErrors(ctx, 1, 500, 10)
	if err != nil {
		t.Fatal(err)
	}
	// InsertUsage 不回填 ID，按插入顺序 r2 是第 2 条。
	if len(errs) != 1 || errs[0].UsageID != 2 || errs[0].Status != 429 || errs[0].Result != "rate_limit" ||
		errs[0].Error != "slow down" || errs[0].CooldownMs != 60000 || errs[0].CreatedAt != 2000 {
		t.Fatalf("errors: %+v", errs)
	}
	// 范围放宽到更早，能看到之前的 500；新的在前。
	errs, _ = s.AccountRecentErrors(ctx, 1, 0, 10)
	if len(errs) != 2 || errs[0].Result != "rate_limit" || errs[1].Result != "server_error" {
		t.Fatalf("all errors: %+v", errs)
	}

	if stats, _ := s.AccountModelStats(ctx, 2, 0); len(stats) != 1 || stats[0].Success != 1 {
		t.Fatalf("account 2: %+v", stats)
	}
}
