package store

import (
	"context"
	"database/sql"
)

// UsageStorage 是用量记录的现状：总条数、最早一条的时间（Unix 毫秒，没有记录时为 0）、早于 cutoff 的条数。
type UsageStorage struct {
	Records  int64 `json:"records"`
	OldestAt int64 `json:"oldest_at"`
	Expired  int64 `json:"expired"`
}

// UsageStorageStats 统计用量记录；cutoff 为 0 时 Expired 为 0。
func (s *Store) UsageStorageStats(ctx context.Context, cutoff int64) (UsageStorage, error) {
	var st UsageStorage
	var oldest sql.NullInt64
	err := s.reader.QueryRowContext(ctx, `
		SELECT count(*), MIN(created_at), COALESCE(SUM(created_at < ?), 0) FROM usage_records`, cutoff).
		Scan(&st.Records, &oldest, &st.Expired)
	st.OldestAt = oldest.Int64
	if cutoff <= 0 {
		st.Expired = 0
	}
	return st, err
}

// PurgeUsageBatch 删除一批早于 cutoff（Unix 毫秒）的用量记录（按 id 取最旧的 limit 条），返回删除的条数；
// 0 表示没有了。删除前在同一个事务里把这些记录的 total_tokens 按 Key 累加到 api_keys.archived_tokens，
// Key 的总量上限不会因为删旧记录而变少。
func (s *Store) PurgeUsageBatch(ctx context.Context, cutoff int64, limit int) (int64, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var maxID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT MAX(id) FROM (SELECT id FROM usage_records WHERE created_at < ? ORDER BY id LIMIT ?)`, cutoff, limit).
		Scan(&maxID); err != nil {
		return 0, err
	}
	if !maxID.Valid {
		return 0, nil
	}
	// 归档和删除用同一个条件，算进去的正好是删掉的。
	if _, err := tx.ExecContext(ctx, `
		UPDATE api_keys SET archived_tokens = archived_tokens + (
		  SELECT COALESCE(SUM(u.total_tokens), 0) FROM usage_records u
		  WHERE u.api_key_id = api_keys.id AND u.created_at < ?1 AND u.id <= ?2)
		WHERE id IN (SELECT DISTINCT api_key_id FROM usage_records WHERE created_at < ?1 AND id <= ?2)`,
		cutoff, maxID.Int64); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM usage_records WHERE created_at < ? AND id <= ?`, cutoff, maxID.Int64)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}
