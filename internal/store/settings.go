package store

import (
	"context"
	"database/sql"
	"errors"
)

// GetSetting 读取一项设置，不存在时 ok 为 false。
func (s *Store) GetSetting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.reader.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetSettings 在一个事务里写入多项设置，已存在的覆盖。
func (s *Store) SetSettings(ctx context.Context, kv map[string]string) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := nowMs()
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO settings (key,value,updated_at) VALUES (?,?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			k, v, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetSetting 写入一项设置，已存在则覆盖。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.execCtx(ctx, `
		INSERT INTO settings (key,value,updated_at) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, nowMs())
	return err
}
