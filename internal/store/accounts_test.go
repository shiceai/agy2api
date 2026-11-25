package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeEncryptor 用可识别的前缀"加密"，便于确认库里存的不是明文。
type fakeEncryptor struct{}

func (fakeEncryptor) Encrypt(s string) (string, error) { return "enc:" + s, nil }
func (fakeEncryptor) Decrypt(s string) (string, error) {
	if !strings.HasPrefix(s, "enc:") {
		return "", errors.New("not encrypted")
	}
	return strings.TrimPrefix(s, "enc:"), nil
}

// 令牌轮换回写只改凭据列，不覆盖同一时刻同步写入的身份、项目、模型目录。
func TestUpdateAccountCredentialsOnlyTouchesCredentials(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	enc := fakeEncryptor{}

	a := &Account{Email: "a@example.com", ProjectID: "p-old", RefreshToken: "rt-1", ClientID: "cid-1", ClientSecret: "cs-1"}
	if err := s.CreateAccount(ctx, enc, a); err != nil {
		t.Fatal(err)
	}
	// 管理端同步改了项目和模型目录。
	a.ProjectID, a.ModelsJSON = "p-new", `[{"id":"gemini-2.5-flash"}]`
	if err := s.UpdateAccountSync(ctx, enc, a); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateAccountCredentials(ctx, enc, a.ID, "rt-2", "cid-2", "cs-2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAccount(ctx, enc, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != "rt-2" || got.ClientID != "cid-2" || got.ClientSecret != "cs-2" {
		t.Fatalf("凭据应已更新，实际 %q %q %q", got.RefreshToken, got.ClientID, got.ClientSecret)
	}
	if got.ProjectID != "p-new" || got.ModelsJSON != a.ModelsJSON || got.Email != "a@example.com" {
		t.Fatalf("非凭据列不应被改动，实际 project=%q models=%q email=%q", got.ProjectID, got.ModelsJSON, got.Email)
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts WHERE refresh_token_enc='enc:rt-2'`); n != 1 {
		t.Fatalf("refresh token 应加密存储")
	}

	if err := s.UpdateAccountCredentials(ctx, enc, 999, "rt", "cid", "cs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("账号不存在时应返回 ErrNotFound，实际 %v", err)
	}
}
