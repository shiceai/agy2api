package store

import (
	"context"
	"fmt"
	"testing"
)

// 被拒上下文：新增、再被拒只更新时间、命中计数、按最近命中排序、过期和超量修剪、单条删除、清空。
func TestRejectedContexts(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for i := 1; i <= 4; i++ {
		c := RejectedContext{Fingerprint: fmt.Sprintf("fp%d", i), Turns: i, APIKeyID: 1, Model: "m", ClientIP: "203.0.113.1", RequestID: fmt.Sprintf("r%d", i), CreatedAt: int64(i * 1000)}
		if err := s.AddRejectedContext(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// 再被拒一次：只更新最近时间，不改首次的请求。
	if err := s.AddRejectedContext(ctx, RejectedContext{Fingerprint: "fp1", Turns: 9, RequestID: "other", CreatedAt: 9000}); err != nil {
		t.Fatal(err)
	}
	if err := s.HitRejectedContext(ctx, "fp2", 8000); err != nil {
		t.Fatal(err)
	}
	list, err := s.RejectedContexts(ctx)
	if err != nil || len(list) != 4 {
		t.Fatalf("应有 4 条：%v %+v", err, list)
	}
	if list[0].Fingerprint != "fp1" || list[0].RequestID != "r1" || list[0].Turns != 1 || list[0].LastHitAt != 9000 ||
		list[1].Fingerprint != "fp2" || list[1].Hits != 1 || list[1].LastHitAt != 8000 {
		t.Fatalf("排序或更新不对：%+v", list[:2])
	}

	// 早于 3500 的删掉（fp3 的 3000，留 3 条时它也正好超出）；再只留 2 条（删最旧的 fp4）。
	if n, err := s.PruneRejectedContexts(ctx, 3500, 3); err != nil || n != 1 {
		t.Fatalf("按时间应删 1 条：%d %v", n, err)
	}
	if n, err := s.PruneRejectedContexts(ctx, 0, 2); err != nil || n != 1 {
		t.Fatalf("按条数应删 1 条：%d %v", n, err)
	}
	if ok, err := s.DeleteRejectedContext(ctx, "fp2"); err != nil || !ok {
		t.Fatalf("删除 fp2：%v %v", ok, err)
	}
	if ok, _ := s.DeleteRejectedContext(ctx, "fp2"); ok {
		t.Fatal("重复删除应返回 false")
	}
	if n, err := s.ClearRejectedContexts(ctx); err != nil || n != 1 {
		t.Fatalf("清空应删剩下的 fp1：%d %v", n, err)
	}
}
