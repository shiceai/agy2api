package store

import (
	"context"
	"database/sql"
)

// 账号详情的统计按每次上游尝试算（用量记录 detail_json 里的 attempts），不按最终请求：换号前失败的尝试
// 只记在最终那条用量下面，但要算进失败账号自己的成功率。用 SQLite 的 json_each 汇总，不另建表；
// 老记录没有尝试明细，不计入。按 created_at 索引限定时间范围，再逐条解析 JSON。

// ModelAttemptStat 是一个账号在某个模型（实际发给上游的模型）上的尝试统计。
type ModelAttemptStat struct {
	Model    string `json:"model"`
	Attempts int64  `json:"attempts"`
	Success  int64  `json:"success"`
	// AvgMs 是成功尝试的平均耗时（毫秒，流式算到流结束），没有成功时为 0。
	AvgMs float64 `json:"avg_ms"`
}

// AccountModelStats 返回账号自 since（Unix 毫秒）以来各模型的尝试统计，按尝试次数从多到少排。
func (s *Store) AccountModelStats(ctx context.Context, accountID, since int64) ([]ModelAttemptStat, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT COALESCE(json_extract(a.value,'$.model'),'') AS model,
		       COUNT(*),
		       SUM(json_extract(a.value,'$.result') = 'success'),
		       COALESCE(AVG(CASE WHEN json_extract(a.value,'$.result') = 'success'
		                         THEN json_extract(a.value,'$.duration_ms') END), 0)
		FROM usage_records u, json_each(NULLIF(u.detail_json,''), '$.attempts') a
		WHERE u.created_at >= ? AND json_extract(a.value,'$.account_id') = ?
		GROUP BY model
		ORDER BY COUNT(*) DESC, model`, since, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelAttemptStat{}
	for rows.Next() {
		var m ModelAttemptStat
		if err := rows.Scan(&m.Model, &m.Attempts, &m.Success, &m.AvgMs); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AccountCostSince 返回最终由这个账号完成的请求自 since 以来的费用合计（十亿分之一美元）；一条都没算过时为 nil。
// 费用只在有 token 的最终记录上，按记录的 account_id 统计，不看尝试明细。
func (s *Store) AccountCostSince(ctx context.Context, accountID, since int64) (*int64, error) {
	var cost sql.NullInt64
	err := s.reader.QueryRowContext(ctx,
		`SELECT SUM(cost_nanos) FROM usage_records WHERE account_id=? AND created_at>=?`, accountID, since).Scan(&cost)
	return nullInt(cost), err
}

// AttemptError 是一次失败的上游尝试，UsageID 是它所在的用量记录（管理端据此打开调用详情）。
type AttemptError struct {
	UsageID    int64  `json:"usage_id"`
	CreatedAt  int64  `json:"created_at"`
	Model      string `json:"model"`
	Status     int    `json:"status"`
	Result     string `json:"result"`
	Error      string `json:"error"`
	CooldownMs int64  `json:"cooldown_ms"`
}

// AccountRecentErrors 返回账号自 since 以来最近的失败尝试，新的在前，最多 limit 条。
func (s *Store) AccountRecentErrors(ctx context.Context, accountID, since int64, limit int) ([]AttemptError, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT u.id, u.created_at,
		       COALESCE(json_extract(a.value,'$.model'),''),
		       COALESCE(json_extract(a.value,'$.status'),0),
		       COALESCE(json_extract(a.value,'$.result'),''),
		       COALESCE(json_extract(a.value,'$.error'),''),
		       COALESCE(json_extract(a.value,'$.cooldown_ms'),0)
		FROM usage_records u, json_each(NULLIF(u.detail_json,''), '$.attempts') a
		WHERE u.created_at >= ? AND json_extract(a.value,'$.account_id') = ?
		  AND json_extract(a.value,'$.result') != 'success'
		ORDER BY u.created_at DESC, json_extract(a.value,'$.try') DESC
		LIMIT ?`, since, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AttemptError{}
	for rows.Next() {
		var e AttemptError
		if err := rows.Scan(&e.UsageID, &e.CreatedAt, &e.Model, &e.Status, &e.Result, &e.Error, &e.CooldownMs); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
