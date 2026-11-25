// Package cryptox 用 AES-256-GCM 加解密敏感凭据（refresh token、client secret）。
// 密钥从配置传入；为空时由调用方在数据目录生成并持久化一份 32 字节随机密钥。
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Cipher 持有一个 AES-GCM 实例。
type Cipher struct {
	aead cipher.AEAD
}

// New 用任意长度的密钥材料构造 Cipher：先 SHA-256 规整为 32 字节。
func New(keyMaterial []byte) (*Cipher, error) {
	if len(keyMaterial) == 0 {
		return nil, errors.New("cryptox: empty key material")
	}
	sum := sha256.Sum256(keyMaterial)
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// keyFile 是自动生成的密钥在数据目录里的文件名。
const keyFile = "agy2api.key"

// LoadOrCreateKey 返回配置里的密钥；为空则读取 dir/agy2api.key，文件不存在时生成 32 字节随机密钥并写入。
//
// 只有文件不存在才生成：读不出来、文件为空都报错，不覆盖原文件。库里的凭据只能用原来的密钥解开，
// 覆盖了就永久丢失。
func LoadOrCreateKey(configured, dir string) ([]byte, error) {
	if configured != "" {
		return []byte(configured), nil
	}
	path := filepath.Join(dir, keyFile)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil && len(raw) > 0:
		return raw, nil
	case err == nil:
		return nil, fmt.Errorf("cryptox: key file %s is empty: restore it from backup (stored credentials need the original key), "+
			"or delete it if this is a fresh install", path)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("cryptox: read key file: %w", err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	if err := writeNewFile(path, key); err != nil {
		return nil, fmt.Errorf("persist encryption key: %w", err)
	}
	return key, nil
}

// writeNewFile 新建文件写入 data 并落盘。文件已存在时报错，不覆盖（O_EXCL）；写失败时删掉写了一半的文件，
// 下次启动能重新生成。
func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

// Encrypt 返回 base64(nonce||ciphertext)。空串原样返回空串。
func (c *Cipher) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 还原 Encrypt 的输出。空串原样返回空串。
func (c *Cipher) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("cryptox: ciphertext too short")
	}
	plain, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
