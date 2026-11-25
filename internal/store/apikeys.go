package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
)

// Token 上限的周期，按服务器时区划分。
const (
	TokenPeriodDay   = "day"
	TokenPeriodMonth = "month"
	TokenPeriodTotal = "total"
)

// APIKey 是客户端调用网关用的下游密钥。库中只存哈希，明文仅在创建时返回一次。
type APIKey struct {
	ID         int64  `json:"id"`
	Prefix     string `json:"prefix"`
	Status     string `json:"status"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
	APIKeySettings
}

// APIKeySettings 是 Key 可修改的名称和限制。
type APIKeySettings struct {
	Name string `json:"name"`
	// ExpiresAt 是到期时间（Unix 毫秒），0 表示永不过期。
	ExpiresAt int64 `json:"expires_at"`
	// TokenLimit 是周期内的 Token 上限（输入 + 输出 + 推理），0 表示不限；TokenPeriod 是 TokenPeriod* 之一。
	TokenLimit  int64  `json:"token_limit"`
	TokenPeriod string `json:"token_period"`
	// AllowedModels 是允许请求的模型名，支持 * 通配，空表示不限制。
	AllowedModels []string `json:"allowed_models"`
}

// HashAPIKey 返回密钥的 SHA-256 十六进制摘要，用于入库与比对。
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// GenerateAPIKey 生成一个随机下游密钥，形如 agy-<43字符>。
func GenerateAPIKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "agy-" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func marshalModels(models []string) string {
	if len(models) == 0 {
		return ""
	}
	raw, _ := json.Marshal(models)
	return string(raw)
}

func unmarshalModels(raw string) []string {
	out := []string{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

// CreateAPIKey 生成并保存一个密钥，返回明文（仅此一次可见）与记录。
func (s *Store) CreateAPIKey(ctx context.Context, settings APIKeySettings) (string, *APIKey, error) {
	plain, err := GenerateAPIKey()
	if err != nil {
		return "", nil, err
	}
	k := &APIKey{
		Prefix:         plain[:12], // agy-xxxxxxxx，用于界面展示
		Status:         StatusEnabled,
		CreatedAt:      nowMs(),
		APIKeySettings: settings,
	}
	res, err := s.execCtx(ctx,
		`INSERT INTO api_keys (name,key_hash,prefix,status,created_at,last_used_at,expires_at,token_limit,token_period,allowed_models)
		 VALUES (?,?,?,?,?,0,?,?,?,?)`,
		k.Name, HashAPIKey(plain), k.Prefix, k.Status, k.CreatedAt,
		k.ExpiresAt, k.TokenLimit, k.TokenPeriod, marshalModels(k.AllowedModels))
	if err != nil {
		return "", nil, err
	}
	if k.ID, err = res.LastInsertId(); err != nil {
		return "", nil, err
	}
	return plain, k, nil
}

// UpdateAPIKey 修改 Key 的名称和限制。
func (s *Store) UpdateAPIKey(ctx context.Context, id int64, settings APIKeySettings) error {
	res, err := s.execCtx(ctx,
		`UPDATE api_keys SET name=?,expires_at=?,token_limit=?,token_period=?,allowed_models=? WHERE id=?`,
		settings.Name, settings.ExpiresAt, settings.TokenLimit, settings.TokenPeriod, marshalModels(settings.AllowedModels), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const apiKeyCols = `id,name,prefix,status,created_at,last_used_at,expires_at,token_limit,token_period,allowed_models`

func scanAPIKey(row interface{ Scan(...any) error }, extra ...any) (*APIKey, error) {
	var k APIKey
	var models string
	dest := append([]any{&k.ID, &k.Name, &k.Prefix, &k.Status, &k.CreatedAt, &k.LastUsedAt,
		&k.ExpiresAt, &k.TokenLimit, &k.TokenPeriod, &models}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	k.AllowedModels = unmarshalModels(models)
	return &k, nil
}

// ListEnabledAPIKeys 返回启用的密钥，按哈希索引，供入站鉴权的内存缓存加载。
func (s *Store) ListEnabledAPIKeys(ctx context.Context) (map[string]*APIKey, error) {
	rows, err := s.reader.QueryContext(ctx,
		`SELECT `+apiKeyCols+`,key_hash FROM api_keys WHERE status=?`, StatusEnabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*APIKey{}
	for rows.Next() {
		var hash string
		k, err := scanAPIKey(rows, &hash)
		if err != nil {
			return nil, err
		}
		out[hash] = k
	}
	return out, rows.Err()
}

// ListAPIKeys 返回全部密钥（不含明文）。
func (s *Store) ListAPIKeys(ctx context.Context) ([]*APIKey, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// SetAPIKeyStatus 启用/停用密钥。
func (s *Store) SetAPIKeyStatus(ctx context.Context, id int64, status string) error {
	_, err := s.execCtx(ctx, `UPDATE api_keys SET status=? WHERE id=?`, status, id)
	return err
}

// DeleteAPIKey 删除密钥。
func (s *Store) DeleteAPIKey(ctx context.Context, id int64) error {
	_, err := s.execCtx(ctx, `DELETE FROM api_keys WHERE id=?`, id)
	return err
}

// TouchAPIKey 更新最近使用时间。调用方尽力而为：失败只记日志，不影响请求。
func (s *Store) TouchAPIKey(ctx context.Context, id int64) error {
	_, err := s.execCtx(ctx, `UPDATE api_keys SET last_used_at=? WHERE id=?`, nowMs(), id)
	return err
}

// APIKeyTokensSince 返回某个 Key 自 since（Unix 毫秒）以来用量记录的 total_tokens 之和，Token 上限计数用。
// since <= 0（总量）时加上清理旧记录时归档的 Token（api_keys.archived_tokens）。
func (s *Store) APIKeyTokensSince(ctx context.Context, id, since int64) (int64, error) {
	var n int64
	err := s.reader.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(total_tokens),0) FROM usage_records WHERE api_key_id=? AND created_at>=?`, id, since).Scan(&n)
	if err != nil || since > 0 {
		return n, err
	}
	var archived int64
	err = s.reader.QueryRowContext(ctx, `SELECT COALESCE((SELECT archived_tokens FROM api_keys WHERE id=?), 0)`, id).Scan(&archived)
	return n + archived, err
}

// APIKeyUsage 是一个 Key 一段时间内的请求数和 Token 数。
type APIKeyUsage struct {
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
	// CostNanos 是算过费用的记录的费用合计；一条都没算过时为 nil。
	CostNanos *int64 `json:"cost_nanos"`
}

// APIKeysUsageSince 返回各 Key 自 since 以来的请求数和 Token 数（列表页"近 30 天"用），按 Key ID 索引。
func (s *Store) APIKeysUsageSince(ctx context.Context, since int64) (map[int64]APIKeyUsage, error) {
	rows, err := s.reader.QueryContext(ctx,
		`SELECT api_key_id, COUNT(*), COALESCE(SUM(total_tokens),0), SUM(cost_nanos) FROM usage_records WHERE created_at>=? GROUP BY api_key_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]APIKeyUsage{}
	for rows.Next() {
		var id int64
		var u APIKeyUsage
		var cost sql.NullInt64
		if err := rows.Scan(&id, &u.Requests, &u.Tokens, &cost); err != nil {
			return nil, err
		}
		u.CostNanos = nullInt(cost)
		out[id] = u
	}
	return out, rows.Err()
}

// KeyModelStat 是一个 Key 在某个请求模型（客户端请求的名字）上的用量，按最终请求统计。
type KeyModelStat struct {
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
	Success  int64  `json:"success"`
	Tokens   int64  `json:"tokens"`
	// AvgMs 是成功请求的平均耗时（毫秒，流式到结束），没有成功时为 0。
	AvgMs float64 `json:"avg_ms"`
	// CostNanos 是算过费用的记录的费用合计；一条都没算过时为 nil。
	CostNanos *int64 `json:"cost_nanos"`
}

// APIKeyModelStats 返回 Key 自 since 以来各请求模型的用量，按请求数从多到少排。
func (s *Store) APIKeyModelStats(ctx context.Context, keyID, since int64) ([]KeyModelStat, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT request_model, COUNT(*),
		       SUM(status_code BETWEEN 200 AND 299),
		       COALESCE(SUM(total_tokens),0),
		       COALESCE(AVG(CASE WHEN status_code BETWEEN 200 AND 299 THEN latency_ms END),0),
		       SUM(cost_nanos)
		FROM usage_records WHERE api_key_id=? AND created_at>=?
		GROUP BY request_model
		ORDER BY COUNT(*) DESC, request_model`, keyID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KeyModelStat{}
	for rows.Next() {
		var m KeyModelStat
		var cost sql.NullInt64
		if err := rows.Scan(&m.Model, &m.Requests, &m.Success, &m.Tokens, &m.AvgMs, &cost); err != nil {
			return nil, err
		}
		m.CostNanos = nullInt(cost)
		out = append(out, m)
	}
	return out, rows.Err()
}
