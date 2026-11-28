package store

import (
	"context"
	"database/sql"
	"errors"
)

// NotifyChannel 是一个告警通知渠道。Config 是明文（已解密）的配置 JSON，含 Webhook 地址、密钥等凭据，
// 不能写进日志或原样返回给接口；格式由 internal/notify 解释。
type NotifyChannel struct {
	ID      int64
	Name    string
	Type    string
	Enabled bool
	Config  string
	// LastSentAt、LastError 是最近一次发送（含测试）的时间和失败原因，成功时 LastError 为空；没发过时 LastSentAt 为 0。
	LastSentAt int64
	LastError  string
	CreatedAt  int64
	UpdatedAt  int64
}

const notifyChannelCols = `id,name,type,enabled,config_enc,last_sent_at,last_error,created_at,updated_at`

func scanNotifyChannel(enc Encryptor, row interface{ Scan(...any) error }) (*NotifyChannel, error) {
	var c NotifyChannel
	var configEnc string
	var enabled int
	if err := row.Scan(&c.ID, &c.Name, &c.Type, &enabled, &configEnc, &c.LastSentAt, &c.LastError, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Enabled = enabled != 0
	var err error
	if c.Config, err = enc.Decrypt(configEnc); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListNotifyChannels 返回全部通知渠道，按 ID 升序。
func (s *Store) ListNotifyChannels(ctx context.Context, enc Encryptor) ([]*NotifyChannel, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT `+notifyChannelCols+` FROM notify_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NotifyChannel
	for rows.Next() {
		c, err := scanNotifyChannel(enc, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetNotifyChannel 按 ID 读取通知渠道。
func (s *Store) GetNotifyChannel(ctx context.Context, enc Encryptor, id int64) (*NotifyChannel, error) {
	c, err := scanNotifyChannel(enc, s.reader.QueryRowContext(ctx, `SELECT `+notifyChannelCols+` FROM notify_channels WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// CreateNotifyChannel 新增一个通知渠道，回填 ID 和时间。
func (s *Store) CreateNotifyChannel(ctx context.Context, enc Encryptor, c *NotifyChannel) error {
	configEnc, err := enc.Encrypt(c.Config)
	if err != nil {
		return err
	}
	now := nowMs()
	res, err := s.execCtx(ctx, `INSERT INTO notify_channels (name,type,enabled,config_enc,created_at,updated_at) VALUES (?,?,?,?,?,?)`,
		c.Name, c.Type, boolInt(c.Enabled), configEnc, now, now)
	if err != nil {
		return err
	}
	if c.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	c.CreatedAt, c.UpdatedAt = now, now
	return nil
}

// UpdateNotifyChannel 修改通知渠道的名称、启用状态和配置（类型不改）。
func (s *Store) UpdateNotifyChannel(ctx context.Context, enc Encryptor, c *NotifyChannel) error {
	configEnc, err := enc.Encrypt(c.Config)
	if err != nil {
		return err
	}
	res, err := s.execCtx(ctx, `UPDATE notify_channels SET name=?, enabled=?, config_enc=?, updated_at=? WHERE id=?`,
		c.Name, boolInt(c.Enabled), configEnc, nowMs(), c.ID)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

// SetNotifyChannelResult 记下一次发送的结果：errText 为空表示成功。
func (s *Store) SetNotifyChannelResult(ctx context.Context, id, at int64, errText string) error {
	_, err := s.execCtx(ctx, `UPDATE notify_channels SET last_sent_at=?, last_error=? WHERE id=?`, at, errText, id)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// requireAffected 在一行都没改到时返回 ErrNotFound。
func requireAffected(res sql.Result) error {
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteNotifyChannel 删除通知渠道。
func (s *Store) DeleteNotifyChannel(ctx context.Context, id int64) error {
	res, err := s.execCtx(ctx, `DELETE FROM notify_channels WHERE id=?`, id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}
