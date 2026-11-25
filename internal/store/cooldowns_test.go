package store

import (
	"context"
	"testing"
)

func TestCooldowns(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a := &Account{Email: "a@example.com", RefreshToken: "rt"}
	b := &Account{Email: "b@example.com", RefreshToken: "rt"}
	for _, acc := range []*Account{a, b} {
		if err := s.CreateAccount(ctx, fakeEncryptor{}, acc); err != nil {
			t.Fatal(err)
		}
	}
	set := func(c Cooldown) {
		t.Helper()
		if err := s.SetCooldowns(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	set(Cooldown{AccountID: a.ID, Model: "m1", Until: 5000, Reason: "rate_limit"})
	set(Cooldown{AccountID: a.ID, Model: "m1", Until: 3000, Reason: "server_error"}) // 更早，不缩短
	set(Cooldown{AccountID: a.ID, Model: "m2", Until: 1000, Reason: "rate_limit"})   // 会过期
	set(Cooldown{AccountID: b.ID, Model: "m1", Until: 9000, Reason: "quota_exhausted"})
	set(Cooldown{AccountID: b.ID, Model: "m1", Until: 9500, Reason: "rate_limit"}) // 更晚，延长并换原因

	got, err := s.ActiveCooldowns(ctx, 2000)
	if err != nil {
		t.Fatal(err)
	}
	want := []Cooldown{
		{AccountID: a.ID, Model: "m1", Until: 5000, Reason: "rate_limit"},
		{AccountID: b.ID, Model: "m1", Until: 9500, Reason: "rate_limit"},
	}
	if len(got) != len(want) {
		t.Fatalf("应有 %d 条，实际 %+v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条：期望 %+v，实际 %+v", i, want[i], got[i])
		}
	}

	// 删除账号时一并删除它的冷却。
	if err := s.DeleteAccount(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.ActiveCooldowns(ctx, 2000); len(got) != 1 || got[0].AccountID != a.ID {
		t.Fatalf("删除账号后应只剩 a 的冷却：%+v", got)
	}
}

// 一次写入、删除多条冷却（同一额度窗口的模型一起冷却、额度恢复后一起解除），在一个事务里完成。
func TestCooldownsBatch(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a := &Account{Email: "a@example.com", RefreshToken: "rt"}
	if err := s.CreateAccount(ctx, fakeEncryptor{}, a); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCooldowns(ctx,
		Cooldown{AccountID: a.ID, Model: "m1", Until: 5000, Reason: "quota_exhausted"},
		Cooldown{AccountID: a.ID, Model: "m2", Until: 5000, Reason: "quota_exhausted"},
		Cooldown{AccountID: a.ID, Model: "m3", Until: 5000, Reason: "quota_exhausted"},
	); err != nil {
		t.Fatal(err)
	}
	// 有一条写失败（账号不存在，外键约束）时整批回滚。
	if err := s.SetCooldowns(ctx,
		Cooldown{AccountID: a.ID, Model: "m4", Until: 5000, Reason: "rate_limit"},
		Cooldown{AccountID: 999, Model: "m1", Until: 5000, Reason: "rate_limit"},
	); err == nil {
		t.Fatal("账号不存在应写入失败")
	}
	if err := s.DeleteCooldowns(ctx, Cooldown{AccountID: a.ID, Model: "m1"}, Cooldown{AccountID: a.ID, Model: "m3"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ActiveCooldowns(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Model != "m2" {
		t.Fatalf("应只剩 m2（m4 随失败的整批回滚），实际 %+v", got)
	}
	if err := s.SetCooldowns(ctx); err != nil {
		t.Fatalf("空批次应直接返回：%v", err)
	}
}

func TestAccountMaxConcurrency(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a := &Account{Email: "a@example.com", RefreshToken: "rt"}
	if err := s.CreateAccount(ctx, fakeEncryptor{}, a); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAccount(ctx, fakeEncryptor{}, a.ID); got.MaxConcurrency != 0 {
		t.Fatalf("默认应为 0（跟随全局），实际 %d", got.MaxConcurrency)
	}
	if err := s.SetAccountMaxConcurrency(ctx, a.ID, 4); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAccount(ctx, fakeEncryptor{}, a.ID); got.MaxConcurrency != 4 {
		t.Fatalf("应读回 4，实际 %d", got.MaxConcurrency)
	}
	if err := s.SetAccountMaxConcurrency(ctx, 999, 4); err != ErrNotFound {
		t.Fatalf("账号不存在应返回 ErrNotFound，实际 %v", err)
	}
}
