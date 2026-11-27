package store

import (
	"context"
	"database/sql"
	"errors"
)

// 失败请求的完整请求体（gzip），见 ddl/20261127_002_request_bodies.sql。

// RequestBodyStats 是已保存的失败请求正文的条数和占用（压缩后的字节数）。
type RequestBodyStats struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// SaveRequestBody 存一条失败请求的正文：gz 是压缩后的内容，size 是压缩前的字节数，createdAt 是 Unix 毫秒。
// 同一请求 ID 已有时覆盖。
func (s *Store) SaveRequestBody(ctx context.Context, requestID string, gz []byte, size, createdAt int64) error {
	_, err := s.execCtx(ctx, `
		INSERT INTO request_bodies (request_id, body_gz, size, created_at) VALUES (?,?,?,?)
		ON CONFLICT(request_id) DO UPDATE SET body_gz=excluded.body_gz, size=excluded.size, created_at=excluded.created_at`,
		requestID, gz, size, createdAt)
	return err
}

// RequestBody 读一条失败请求的正文（压缩的），没有时返回 ErrNotFound。
func (s *Store) RequestBody(ctx context.Context, requestID string) (gz []byte, size int64, err error) {
	err = s.reader.QueryRowContext(ctx, `SELECT body_gz, size FROM request_bodies WHERE request_id=?`, requestID).Scan(&gz, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	return gz, size, err
}

// PruneRequestBodies 只留最新的 maxCount 条、且按新到旧累计占用不超过 maxBytes 的部分，删掉其余，返回删除的条数。
func (s *Store) PruneRequestBodies(ctx context.Context, maxCount int, maxBytes int64) (int64, error) {
	res, err := s.execCtx(ctx, `
		DELETE FROM request_bodies WHERE request_id IN (
		  SELECT request_id FROM (
		    SELECT request_id,
		           ROW_NUMBER() OVER (ORDER BY created_at DESC, request_id DESC) AS n,
		           SUM(length(body_gz)) OVER (ORDER BY created_at DESC, request_id DESC) AS total
		    FROM request_bodies)
		  WHERE n > ? OR total > ?)`, maxCount, maxBytes)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteRequestBodiesBefore 删除 cutoff（Unix 毫秒）之前保存的正文（随用量记录一起清理），返回删除的条数。
func (s *Store) DeleteRequestBodiesBefore(ctx context.Context, cutoff int64) (int64, error) {
	res, err := s.execCtx(ctx, `DELETE FROM request_bodies WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearRequestBodies 删除全部已保存的正文，返回删除的条数。
func (s *Store) ClearRequestBodies(ctx context.Context) (int64, error) {
	res, err := s.execCtx(ctx, `DELETE FROM request_bodies`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RequestBodyStats 返回已保存的正文的条数和占用。
func (s *Store) RequestBodyStats(ctx context.Context) (RequestBodyStats, error) {
	var st RequestBodyStats
	err := s.reader.QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(length(body_gz)),0) FROM request_bodies`).Scan(&st.Count, &st.Bytes)
	return st, err
}
