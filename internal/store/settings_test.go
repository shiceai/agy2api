package store

import (
	"context"
	"testing"
)

func TestSettings(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	if _, ok, err := s.GetSetting(ctx, "default_tier"); err != nil || ok {
		t.Fatalf("未设置时应返回 ok=false，实际 %v %v", ok, err)
	}
	for _, v := range []string{"high", "low"} {
		if err := s.SetSetting(ctx, "default_tier", v); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := s.GetSetting(ctx, "default_tier"); err != nil || !ok || got != v {
			t.Fatalf("应读到 %q，实际 %q %v %v", v, got, ok, err)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM settings`); n != 1 {
		t.Fatalf("重复写同一项应覆盖，实际 %d 行", n)
	}
}
