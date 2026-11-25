package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAPIKeySettingsRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()

	_, k, err := s.CreateAPIKey(ctx, APIKeySettings{Name: "a", ExpiresAt: 123, TokenLimit: 1000, TokenPeriod: TokenPeriodMonth,
		AllowedModels: []string{"gemini-*", "claude-sonnet-5-5"}})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListAPIKeys(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	if got := list[0].APIKeySettings; !reflect.DeepEqual(got, k.APIKeySettings) {
		t.Fatalf("round trip: %+v != %+v", got, k.APIKeySettings)
	}

	// 清空限制：模型列表读回来是空切片（JSON 里是 []，不是 null）。
	if err := s.UpdateAPIKey(ctx, k.ID, APIKeySettings{Name: "b"}); err != nil {
		t.Fatal(err)
	}
	enabled, _ := s.ListEnabledAPIKeys(ctx)
	for _, e := range enabled {
		if e.Name != "b" || e.TokenLimit != 0 || e.AllowedModels == nil || len(e.AllowedModels) != 0 {
			t.Fatalf("after clearing: %+v", e)
		}
	}
	if err := s.UpdateAPIKey(ctx, 999, APIKeySettings{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown key: %v", err)
	}

	// 用量汇总
	recs := []*UsageRecord{
		{RequestID: "r1", APIKeyID: k.ID, StatusCode: 200, TotalTokens: 10, CreatedAt: 1000},
		{RequestID: "r2", APIKeyID: k.ID, StatusCode: 200, TotalTokens: 20, CreatedAt: 2000},
		{RequestID: "r3", APIKeyID: 0, StatusCode: 200, TotalTokens: 7, CreatedAt: 2000},
	}
	if err := s.InsertUsage(ctx, recs...); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.APIKeyTokensSince(ctx, k.ID, 1500); n != 20 {
		t.Fatalf("tokens since: %d", n)
	}
	usage, _ := s.APIKeysUsageSince(ctx, 0)
	if usage[k.ID] != (APIKeyUsage{Requests: 2, Tokens: 30}) || usage[0] != (APIKeyUsage{Requests: 1, Tokens: 7}) {
		t.Fatalf("usage since: %+v", usage)
	}
}

func TestAPIKeyModelStats(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	recs := []*UsageRecord{
		{RequestID: "a", APIKeyID: 1, RequestModel: "flash", StatusCode: 200, TotalTokens: 10, LatencyMs: 100, CreatedAt: 1000},
		{RequestID: "b", APIKeyID: 1, RequestModel: "flash", StatusCode: 429, LatencyMs: 5, CreatedAt: 1000},
		{RequestID: "c", APIKeyID: 1, RequestModel: "pro", StatusCode: 200, TotalTokens: 30, LatencyMs: 300, CreatedAt: 1000},
		{RequestID: "d", APIKeyID: 2, RequestModel: "flash", StatusCode: 200, TotalTokens: 99, CreatedAt: 1000},
		{RequestID: "e", APIKeyID: 1, RequestModel: "flash", StatusCode: 200, TotalTokens: 99, CreatedAt: 10},
	}
	if err := s.InsertUsage(ctx, recs...); err != nil {
		t.Fatal(err)
	}
	stats, err := s.APIKeyModelStats(ctx, 1, 500)
	if err != nil {
		t.Fatal(err)
	}
	want := []KeyModelStat{
		{Model: "flash", Requests: 2, Success: 1, Tokens: 10, AvgMs: 100},
		{Model: "pro", Requests: 1, Success: 1, Tokens: 30, AvgMs: 300},
	}
	if !reflect.DeepEqual(stats, want) {
		t.Fatalf("stats: %+v", stats)
	}
}
