package store

import "context"

// Cooldown 是一条账号 × 模型的冷却：截止前该账号不接这个模型的请求。
type Cooldown struct {
	AccountID int64
	Model     string // 解析后实际发给上游的模型 ID
	Until     int64  // Unix 毫秒
	Reason    string // 分类，如 rate_limit、quota_exhausted、validation_required
}

// SetCooldowns 在一个事务里写入若干条冷却（额度耗尽时同一额度窗口的模型一起写）。已有记录时只延长不缩短：
// 截止时间取较晚者，原因随较晚的那条。
func (s *Store) SetCooldowns(ctx context.Context, cs ...Cooldown) error {
	if len(cs) == 0 {
		return nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO account_cooldowns (account_id,model,until_at,reason,created_at) VALUES (?,?,?,?,?)
		ON CONFLICT(account_id,model) DO UPDATE SET
		  reason=CASE WHEN excluded.until_at > until_at THEN excluded.reason ELSE reason END,
		  until_at=MAX(until_at, excluded.until_at)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := nowMs()
	for _, c := range cs {
		if _, err := stmt.ExecContext(ctx, c.AccountID, c.Model, c.Until, c.Reason, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RelaxCooldown 把一条冷却的截止时间从 from 改成更早的 c.Until、原因改成 c.Reason（都是 Unix 毫秒）：只在记录的截止时间
// 还是 from 时改，期间被别处延长过就不动。事后发现冷却设重了时用（见 relay 的请求被拒）。
func (s *Store) RelaxCooldown(ctx context.Context, c Cooldown, from int64) error {
	_, err := s.execCtx(ctx, `UPDATE account_cooldowns SET until_at=?, reason=? WHERE account_id=? AND model=? AND until_at=?`,
		c.Until, c.Reason, c.AccountID, c.Model, from)
	return err
}

// ActiveCooldowns 删除 now（Unix 毫秒）之前已过期的冷却，返回其余的。
func (s *Store) ActiveCooldowns(ctx context.Context, now int64) ([]Cooldown, error) {
	if _, err := s.execCtx(ctx, `DELETE FROM account_cooldowns WHERE until_at <= ?`, now); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx,
		`SELECT account_id,model,until_at,reason FROM account_cooldowns ORDER BY account_id, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Cooldown
	for rows.Next() {
		var c Cooldown
		if err := rows.Scan(&c.AccountID, &c.Model, &c.Until, &c.Reason); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteCooldowns 在一个事务里删除若干条冷却（额度恢复后提前解除时用），按 AccountID、Model 定位，其余字段不用。
func (s *Store) DeleteCooldowns(ctx context.Context, cs ...Cooldown) error {
	if len(cs) == 0 {
		return nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `DELETE FROM account_cooldowns WHERE account_id=? AND model=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range cs {
		if _, err := stmt.ExecContext(ctx, c.AccountID, c.Model); err != nil {
			return err
		}
	}
	return tx.Commit()
}
