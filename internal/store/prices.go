package store

import (
	"context"
	"encoding/json"
)

// ModelPrice 是 model_prices 的一行。PriceJSON 的结构由 pricing 包定义，这里只存取。
type ModelPrice struct {
	Model     string
	Source    string
	Vendor    string
	Name      string
	PriceJSON string
	UpdatedAt int64
}

// ListModelPrices 返回全部价格。
func (s *Store) ListModelPrices(ctx context.Context) ([]ModelPrice, error) {
	rows, err := s.reader.QueryContext(ctx,
		`SELECT model, source, vendor, name, price_json, updated_at FROM model_prices ORDER BY model, source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelPrice
	for rows.Next() {
		var p ModelPrice
		if err := rows.Scan(&p.Model, &p.Source, &p.Vendor, &p.Name, &p.PriceJSON, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertModelPrices 在一个事务里写入若干条价格，同一 (model, source) 已存在的覆盖。UpdatedAt 为 0 时用当前时间。
func (s *Store) UpsertModelPrices(ctx context.Context, prices []ModelPrice) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := nowMs()
	for _, p := range prices {
		at := p.UpdatedAt
		if at == 0 {
			at = now
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO model_prices (model, source, vendor, name, price_json, updated_at) VALUES (?,?,?,?,?,?)
			ON CONFLICT(model, source) DO UPDATE SET vendor=excluded.vendor, name=excluded.name,
			  price_json=excluded.price_json, updated_at=excluded.updated_at`,
			p.Model, p.Source, p.Vendor, p.Name, p.PriceJSON, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteModelPrice 删除一条价格，不存在时返回 ErrNotFound。
func (s *Store) DeleteModelPrice(ctx context.Context, model, source string) error {
	res, err := s.execCtx(ctx, `DELETE FROM model_prices WHERE model=? AND source=?`, model, source)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UnpricedUsage 是一条还没算费用、有 token 的用量记录，补算费用用。
type UnpricedUsage struct {
	ID int64
	// Model 是实际发给上游的模型；没有这一项的老记录（模型原样透传的时期）用请求的模型名。
	Model     string
	CreatedAt int64
	Prompt    int64
	Cached    int64
	Output    int64
	Thoughts  int64
}

// ListUnpricedUsage 返回 id 大于 afterID、还没算费用、有 token 的用量记录，按 id 升序，最多 limit 条。
func (s *Store) ListUnpricedUsage(ctx context.Context, afterID int64, limit int) ([]UnpricedUsage, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT id, COALESCE(NULLIF(resolved_model,''), request_model), created_at, prompt_tokens, COALESCE(cached_tokens,0), output_tokens,
		       COALESCE(thoughts_tokens,0)
		FROM usage_records WHERE cost_nanos IS NULL AND total_tokens > 0 AND id > ?
		ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnpricedUsage
	for rows.Next() {
		var u UnpricedUsage
		if err := rows.Scan(&u.ID, &u.Model, &u.CreatedAt, &u.Prompt, &u.Cached, &u.Output, &u.Thoughts); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UsageCostUpdate 是补算出的一条费用：Cost 写进 detail_json 的 cost。
type UsageCostUpdate struct {
	ID   int64
	Cost *UsageCost
}

// SetUsageCosts 在一个事务里补写费用，只改还没算过的记录（期间被别处写过的不覆盖）。
func (s *Store) SetUsageCosts(ctx context.Context, updates []UsageCostUpdate) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		UPDATE usage_records SET cost_nanos = ?,
		  detail_json = json_set(COALESCE(NULLIF(detail_json,''),'{}'), '$.cost', json(?))
		WHERE id = ? AND cost_nanos IS NULL`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, u := range updates {
		raw, err := json.Marshal(u.Cost)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, u.Cost.Nanos, string(raw), u.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
