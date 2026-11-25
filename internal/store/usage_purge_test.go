package store

import (
	"context"
	"testing"
)

// 清理旧记录：只删早于 cutoff 的，分批删完；删掉的 Token 归档到 Key，总量用量不变，按时间段的用量只看现存记录。
func TestPurgeUsage(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	_, k, err := s.CreateAPIKey(ctx, APIKeySettings{Name: "k"})
	if err != nil {
		t.Fatal(err)
	}
	var recs []*UsageRecord
	for i := range 5 {
		recs = append(recs, &UsageRecord{RequestID: "old", APIKeyID: k.ID, TotalTokens: 100, StatusCode: 200, CreatedAt: int64(1000 + i)})
	}
	recs = append(recs,
		&UsageRecord{RequestID: "new", APIKeyID: k.ID, TotalTokens: 7, StatusCode: 200, CreatedAt: 5000},
		&UsageRecord{RequestID: "other", APIKeyID: 999, TotalTokens: 1, StatusCode: 200, CreatedAt: 1001})
	if err := s.InsertUsage(ctx, recs...); err != nil {
		t.Fatal(err)
	}
	totalBefore, _ := s.APIKeyTokensSince(ctx, k.ID, 0)

	st, err := s.UsageStorageStats(ctx, 2000)
	if err != nil || st.Records != 7 || st.OldestAt != 1000 || st.Expired != 6 {
		t.Fatalf("stats: %+v %v", st, err)
	}
	var deleted int64
	for {
		n, err := s.PurgeUsageBatch(ctx, 2000, 2)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		deleted += n
	}
	if deleted != 6 {
		t.Fatalf("deleted %d", deleted)
	}
	if st, _ := s.UsageStorageStats(ctx, 2000); st.Records != 1 || st.Expired != 0 || st.OldestAt != 5000 {
		t.Fatalf("after purge: %+v", st)
	}
	if total, _ := s.APIKeyTokensSince(ctx, k.ID, 0); total != totalBefore || total != 507 {
		t.Fatalf("total usage changed: %d -> %d", totalBefore, total)
	}
	if recent, _ := s.APIKeyTokensSince(ctx, k.ID, 3000); recent != 7 {
		t.Fatalf("recent usage: %d", recent)
	}
}
