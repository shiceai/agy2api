package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
)

// 失败请求正文：存、读、覆盖；按条数和总占用修剪（留最新的）；按时间删；清空。
func TestRequestBodies(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, _, err := s.RequestBody(ctx, "none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("没有时应返回 ErrNotFound：%v", err)
	}
	for i := 1; i <= 5; i++ {
		gz := bytes.Repeat([]byte{byte(i)}, 100)
		if err := s.SaveRequestBody(ctx, fmt.Sprintf("r%d", i), gz, 1000, int64(i*1000)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveRequestBody(ctx, "r5", bytes.Repeat([]byte{9}, 100), 2000, 5000); err != nil {
		t.Fatal(err)
	}
	gz, size, err := s.RequestBody(ctx, "r5")
	if err != nil || size != 2000 || gz[0] != 9 {
		t.Fatalf("同一请求 ID 应覆盖：%v %d %v", err, size, gz[:1])
	}
	if st, _ := s.RequestBodyStats(ctx); st.Count != 5 || st.Bytes != 500 {
		t.Fatalf("统计：%+v", st)
	}

	// 条数上限 4：删最旧的 r1。
	if n, err := s.PruneRequestBodies(ctx, 4, 1<<20); err != nil || n != 1 {
		t.Fatalf("按条数修剪应删 1 条：%d %v", n, err)
	}
	if _, _, err := s.RequestBody(ctx, "r1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("应删掉最旧的 r1")
	}
	// 总占用上限 250 字节：从新到旧累计，r5、r4 共 200 留下，r3 起超出。
	if n, err := s.PruneRequestBodies(ctx, 100, 250); err != nil || n != 2 {
		t.Fatalf("按占用修剪应删 2 条：%d %v", n, err)
	}
	if st, _ := s.RequestBodyStats(ctx); st.Count != 2 || st.Bytes != 200 {
		t.Fatalf("修剪后应剩 r4、r5：%+v", st)
	}
	if n, err := s.DeleteRequestBodiesBefore(ctx, 4500); err != nil || n != 1 {
		t.Fatalf("按时间应删 r4：%d %v", n, err)
	}
	if n, err := s.ClearRequestBodies(ctx); err != nil || n != 1 {
		t.Fatalf("清空应删剩下的 1 条：%d %v", n, err)
	}
}
