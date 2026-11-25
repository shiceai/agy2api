package relay

import (
	"context"
	"fmt"
	"testing"

	"github.com/shiceai/agy2api/internal/store"
)

// 多于一批的记录都能落库；Close 写完队列后返回，可重复调用；关闭后再写入不 panic、直接丢弃。
func TestUsageWriterDrainsOnClose(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	w := newUsageWriter(st)

	const n = usageBatchSize*2 + 7
	for i := 0; i < n; i++ {
		w.Enqueue(&store.UsageRecord{RequestID: fmt.Sprintf("req-%d", i), RequestModel: "m", StatusCode: 200, CreatedAt: int64(i + 1)})
	}
	ctx := context.Background()
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatalf("重复 Close 应返回 nil，实际 %v", err)
	}
	w.Enqueue(&store.UsageRecord{RequestID: "after-close"})

	records, err := st.RecentUsage(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != n {
		t.Fatalf("应写入 %d 条，实际 %d", n, len(records))
	}
	for _, r := range records {
		if r.RequestID == "after-close" {
			t.Fatal("关闭后的记录不应写入")
		}
		if r.CreatedAt == 0 {
			t.Fatal("CreatedAt 应使用请求结束时间")
		}
	}
}
