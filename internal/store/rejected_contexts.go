package store

import "context"

// 被上游拒绝的上下文指纹，见 ddl/20261127_003_rejected_contexts.sql。

// RejectedContext 是一段被上游拒绝的上下文：指纹和首次被拒的请求，以及之后被拦下的次数。时间是 Unix 毫秒。
type RejectedContext struct {
	Fingerprint string `json:"fingerprint"`
	Turns       int    `json:"turns"`
	APIKeyID    int64  `json:"api_key_id"`
	Model       string `json:"model"`
	ClientIP    string `json:"client_ip"`
	RequestID   string `json:"request_id"`
	Hits        int64  `json:"hits"`
	CreatedAt   int64  `json:"created_at"`
	LastHitAt   int64  `json:"last_hit_at"`
}

// AddRejectedContext 记下一段被拒的上下文；同一指纹已有时只把最近时间更新为 c.CreatedAt（又被拒了一次）。
func (s *Store) AddRejectedContext(ctx context.Context, c RejectedContext) error {
	_, err := s.execCtx(ctx, `
		INSERT INTO rejected_contexts (fingerprint, turns, api_key_id, model, client_ip, request_id, hits, created_at, last_hit_at)
		VALUES (?,?,?,?,?,?,0,?,?)
		ON CONFLICT(fingerprint) DO UPDATE SET last_hit_at=MAX(last_hit_at, excluded.last_hit_at)`,
		c.Fingerprint, c.Turns, c.APIKeyID, c.Model, c.ClientIP, c.RequestID, c.CreatedAt, c.CreatedAt)
	return err
}

// HitRejectedContext 记一次拦截：命中次数加一，最近时间改成 at。
func (s *Store) HitRejectedContext(ctx context.Context, fingerprint string, at int64) error {
	_, err := s.execCtx(ctx, `UPDATE rejected_contexts SET hits=hits+1, last_hit_at=MAX(last_hit_at, ?) WHERE fingerprint=?`, at, fingerprint)
	return err
}

// RejectedContexts 返回全部被拒的上下文，最近命中的在前。
func (s *Store) RejectedContexts(ctx context.Context) ([]RejectedContext, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT fingerprint, turns, api_key_id, model, client_ip, request_id, hits, created_at, last_hit_at
		FROM rejected_contexts ORDER BY last_hit_at DESC, fingerprint`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RejectedContext
	for rows.Next() {
		var c RejectedContext
		if err := rows.Scan(&c.Fingerprint, &c.Turns, &c.APIKeyID, &c.Model, &c.ClientIP, &c.RequestID, &c.Hits, &c.CreatedAt, &c.LastHitAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteRejectedContext 删除一段被拒的上下文，返回是否删到。
func (s *Store) DeleteRejectedContext(ctx context.Context, fingerprint string) (bool, error) {
	res, err := s.execCtx(ctx, `DELETE FROM rejected_contexts WHERE fingerprint=?`, fingerprint)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// PruneRejectedContexts 删除最近时间早于 before 的，再只留最近的 maxCount 条，返回删除的条数。
func (s *Store) PruneRejectedContexts(ctx context.Context, before int64, maxCount int) (int64, error) {
	res, err := s.execCtx(ctx, `
		DELETE FROM rejected_contexts WHERE last_hit_at < ? OR fingerprint IN (
		  SELECT fingerprint FROM rejected_contexts ORDER BY last_hit_at DESC, fingerprint LIMIT -1 OFFSET ?)`,
		before, maxCount)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearRejectedContexts 删除全部被拒的上下文，返回删除的条数。
func (s *Store) ClearRejectedContexts(ctx context.Context) (int64, error) {
	res, err := s.execCtx(ctx, `DELETE FROM rejected_contexts`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
