package store

import (
	"context"
	"testing"
)

// usageFixture 入库一个账号、一个 Key 和若干用量记录，created_at 依次为 1000、2000……
func usageFixture(t *testing.T) (*Store, int64, int64) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	acc := &Account{Email: "a@example.com", RefreshToken: "rt"}
	if err := s.CreateAccount(ctx, fakeEncryptor{}, acc); err != nil {
		t.Fatal(err)
	}
	_, key, err := s.CreateAPIKey(ctx, APIKeySettings{Name: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	recs := []*UsageRecord{
		{RequestID: "r1", AccountID: acc.ID, APIKeyID: key.ID, RequestModel: "gemini-2.5-flash", StatusCode: 200, PromptTokens: 10, OutputTokens: 5, TotalTokens: 15, LatencyMs: 100},
		{RequestID: "r2", AccountID: acc.ID, APIKeyID: key.ID, RequestModel: "gemini-2.5-flash", StatusCode: 429, LatencyMs: 50, Stream: true},
		{RequestID: "r3", AccountID: acc.ID, APIKeyID: key.ID, RequestModel: "claude-opus-5-5", StatusCode: 200, TotalTokens: 30, LatencyMs: 300, Stream: true},
		{RequestID: "r4", AccountID: 99, APIKeyID: 98, RequestModel: "gemini-2.5-flash", StatusCode: 200, TotalTokens: 7, LatencyMs: 200},
	}
	for i, r := range recs {
		r.CreatedAt = int64(i+1) * 1000
	}
	if err := s.InsertUsage(ctx, recs...); err != nil {
		t.Fatal(err)
	}
	return s, acc.ID, key.ID
}

func TestUsageLogsFilters(t *testing.T) {
	s, accID, keyID := usageFixture(t)
	ctx := context.Background()
	// 0 是账号检测：夹具里没有检测记录，按 0 筛选应该一条都没有，而不是当成不筛选。
	probeKey := int64(0)
	cases := []struct {
		name string
		f    UsageFilter
		want []string // 期望的 request_id，按 id 倒序
	}{
		{"全部", UsageFilter{}, []string{"r4", "r3", "r2", "r1"}},
		{"时间范围", UsageFilter{Since: 2000, Until: 4000}, []string{"r3", "r2"}},
		{"成功", UsageFilter{Status: "success"}, []string{"r4", "r3", "r1"}},
		{"错误", UsageFilter{Status: "error"}, []string{"r2"}},
		{"模型", UsageFilter{Model: "claude-opus-5-5"}, []string{"r3"}},
		{"账号", UsageFilter{AccountID: accID}, []string{"r3", "r2", "r1"}},
		{"Key", UsageFilter{APIKeyID: &keyID, Stream: "stream"}, []string{"r3", "r2"}},
		{"账号检测（Key 为 0）", UsageFilter{APIKeyID: &probeKey}, nil},
		{"非流式", UsageFilter{Stream: "sync"}, []string{"r4", "r1"}},
		{"请求 ID", UsageFilter{RequestID: "r2"}, []string{"r2"}},
	}
	for _, tc := range cases {
		logs, total, err := s.UsageLogs(ctx, tc.f, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, l := range logs {
			got = append(got, l.RequestID)
		}
		if total != int64(len(tc.want)) || len(got) != len(tc.want) {
			t.Fatalf("%s：期望 %v，实际 %v（total=%d）", tc.name, tc.want, got, total)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s：期望 %v，实际 %v", tc.name, tc.want, got)
			}
		}
	}

	// 分页：total 是全部符合条件的条数。
	logs, total, err := s.UsageLogs(ctx, UsageFilter{}, 2, 2)
	if err != nil || total != 4 || len(logs) != 2 || logs[0].RequestID != "r2" {
		t.Fatalf("分页不对：%v total=%d err=%v", logs, total, err)
	}
	// 带账号邮箱和 Key 名称；账号、Key 已删除时为空。
	all, _, _ := s.UsageLogs(ctx, UsageFilter{}, 50, 0)
	if all[3].AccountEmail != "a@example.com" || all[3].APIKeyName != "cli" || all[3].APIKeyPrefix == "" {
		t.Fatalf("应带账号邮箱和 Key 名称：%+v", all[3])
	}
	if all[0].AccountEmail != "" || all[0].APIKeyName != "" {
		t.Fatalf("账号、Key 不存在时应为空：%+v", all[0])
	}
}

// 新增的明细字段：有值的原样读回，NULL 读回为 nil（老记录、非流式没有首字延迟）。
func TestUsageDetailFields(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	i64 := func(v int64) *int64 { return &v }
	full := &UsageRecord{RequestID: "full", RequestModel: "gemini-3.8-flash", ResolvedModel: "gemini-3.8-flash-high",
		UpstreamModel: "gemini-3.8-flash-n", Reasoning: "high", PromptTokens: 100, CachedTokens: i64(60),
		ThoughtsTokens: i64(30), OutputTokens: 10, TotalTokens: 140, StatusCode: 200, Stream: true,
		FirstTokenMs: i64(250), LatencyMs: 1200}
	bare := &UsageRecord{RequestID: "bare", RequestModel: "gemini-2.5-flash", StatusCode: 200}
	if err := s.InsertUsage(ctx, full, bare); err != nil {
		t.Fatal(err)
	}
	logs, _, err := s.UsageLogs(ctx, UsageFilter{}, 10, 0)
	if err != nil || len(logs) != 2 {
		t.Fatalf("%v %v", logs, err)
	}
	got, empty := logs[1].UsageRecord, logs[0].UsageRecord
	if got.ResolvedModel != full.ResolvedModel || got.UpstreamModel != full.UpstreamModel || got.Reasoning != "high" ||
		*got.CachedTokens != 60 || *got.ThoughtsTokens != 30 || *got.FirstTokenMs != 250 || !got.Stream {
		t.Fatalf("明细字段没读回：%+v", got)
	}
	if empty.CachedTokens != nil || empty.ThoughtsTokens != nil || empty.FirstTokenMs != nil || empty.Reasoning != "" {
		t.Fatalf("没有数据的字段应为空：%+v", empty)
	}
	recent, err := s.RecentUsage(ctx, 10)
	if err != nil || len(recent) != 2 || *recent[1].ThoughtsTokens != 30 {
		t.Fatalf("RecentUsage 也应读出明细：%v %v", recent, err)
	}
}

// 按小时聚合的分档从 since 起算：since 不在整点（如 +5:30 时区的本地零点）时，一档是 since 起的一小时。
func TestUsageHourlyAlignsToSince(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	const h = int64(3600_000)
	since := 100*h + h/2
	if err := s.InsertUsage(ctx,
		&UsageRecord{RequestModel: "m", StatusCode: 200, CreatedAt: since},
		&UsageRecord{RequestModel: "m", StatusCode: 200, CreatedAt: since + h - 1},
		&UsageRecord{RequestModel: "m", StatusCode: 200, CreatedAt: since + h},
	); err != nil {
		t.Fatal(err)
	}
	got, err := s.UsageHourly(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Start != since || got[0].Requests != 2 || got[1].Start != since+h || got[1].Requests != 1 {
		t.Fatalf("应从 since 起每小时一档：%+v %+v", got[0], got[1:])
	}
}

// 按小时聚合：同一小时的记录合并，非 2xx 计入错误，since 之前的不算，空小时不返回。
func TestUsageHourly(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	const h = int64(3600_000)
	base := 100 * h
	recs := []*UsageRecord{
		{RequestModel: "m", StatusCode: 200, PromptTokens: 1, OutputTokens: 2, TotalTokens: 3, CreatedAt: base - 1},
		{RequestModel: "m", StatusCode: 200, PromptTokens: 10, OutputTokens: 5, TotalTokens: 15, CreatedAt: base},
		{RequestModel: "m", StatusCode: 429, CreatedAt: base + h - 1},
		{RequestModel: "m", StatusCode: 200, PromptTokens: 4, OutputTokens: 6, TotalTokens: 10, CreatedAt: base + 2*h + 5},
	}
	if err := s.InsertUsage(ctx, recs...); err != nil {
		t.Fatal(err)
	}
	got, err := s.UsageHourly(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	want := []HourlyUsage{
		{Start: base, Requests: 2, Errors: 1, PromptTokens: 10, OutputTokens: 5, TotalTokens: 15},
		{Start: base + 2*h, Requests: 1, PromptTokens: 4, OutputTokens: 6, TotalTokens: 10},
	}
	if len(got) != len(want) {
		t.Fatalf("应有 %d 个小时，实际 %d：%+v", len(want), len(got), got)
	}
	for i := range want {
		if *got[i] != want[i] {
			t.Fatalf("第 %d 个小时：期望 %+v，实际 %+v", i, want[i], *got[i])
		}
	}
}
