package cryptox

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func newCipher(t *testing.T, key string) *Cipher {
	t.Helper()
	c, err := New([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// 加密后能解回原文；空串原样进出；同一明文每次密文不同（随机 nonce）。
func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := newCipher(t, "key-material")
	for _, plain := range []string{"1//refresh-token", "带中文的 secret", ""} {
		enc, err := c.Encrypt(plain)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Decrypt(enc)
		if err != nil || got != plain {
			t.Fatalf("%q 解密结果不对：%q %v", plain, got, err)
		}
	}
	if enc, _ := c.Encrypt(""); enc != "" {
		t.Fatalf("空串应加密成空串，实际 %q", enc)
	}
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Fatal("同一明文两次加密的密文不应相同")
	}
}

// 密文被改、换了密钥、格式不对都解不开。
func TestDecryptRejectsBadInput(t *testing.T) {
	c := newCipher(t, "key-material")
	enc, err := c.Encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(enc)
	raw[len(raw)-1] ^= 1
	tampered := base64.StdEncoding.EncodeToString(raw)

	cases := map[string]struct {
		c   *Cipher
		enc string
	}{
		"密文被改":      {c, tampered},
		"换了密钥":      {newCipher(t, "other-key"), enc},
		"不是 base64": {c, "not base64!"},
		"太短":        {c, base64.StdEncoding.EncodeToString([]byte("short"))},
	}
	for name, tc := range cases {
		if got, err := tc.c.Decrypt(tc.enc); err == nil {
			t.Errorf("%s：应解密失败，实际得到 %q", name, got)
		}
	}
	if _, err := New(nil); err == nil {
		t.Fatal("空密钥材料应报错")
	}
}

// 配置了密钥时直接用，不碰数据目录；没配置时首次生成 32 字节并写文件，之后读回同一个。
func TestLoadOrCreateKey(t *testing.T) {
	dir := t.TempDir()
	key, err := LoadOrCreateKey("configured", dir)
	if err != nil || string(key) != "configured" {
		t.Fatalf("应使用配置的密钥：%q %v", key, err)
	}
	if _, err := os.Stat(filepath.Join(dir, keyFile)); !os.IsNotExist(err) {
		t.Fatalf("配置了密钥时不应生成密钥文件：%v", err)
	}

	dataDir := filepath.Join(dir, "data") // 数据目录还不存在
	first, err := LoadOrCreateKey("", dataDir)
	if err != nil || len(first) != 32 {
		t.Fatalf("应生成 32 字节密钥：%d %v", len(first), err)
	}
	second, err := LoadOrCreateKey("", dataDir)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("再次加载应读回同一个密钥：%v", err)
	}
}

// 密钥文件为空或读不出来时报错，不生成新密钥覆盖它：库里的凭据只能用原密钥解开。
func TestLoadOrCreateKeyNeverOverwrites(t *testing.T) {
	t.Run("文件为空", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, keyFile)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateKey("", dir); err == nil {
			t.Fatal("密钥文件为空时应报错")
		}
		if raw, _ := os.ReadFile(path); len(raw) != 0 {
			t.Fatalf("不应覆盖原密钥文件，实际写入了 %d 字节", len(raw))
		}
	})
	t.Run("读不出来", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, keyFile)
		if err := os.Mkdir(path, 0o700); err != nil { // 同名目录：读文件出错，但不是"不存在"
			t.Fatal(err)
		}
		if _, err := LoadOrCreateKey("", dir); err == nil {
			t.Fatal("读不出密钥文件时应报错")
		}
		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			t.Fatalf("不应动原来的路径：%v", err)
		}
	})
}
