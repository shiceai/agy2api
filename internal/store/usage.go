package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// UsageRecord 是一次网关请求的用量记录。指针字段为 nil 表示没有这项数据（老记录，或非流式没有首字延迟）。
type UsageRecord struct {
	ID        int64  `json:"id"`
	RequestID string `json:"request_id"`
	APIKeyID  int64  `json:"api_key_id"`
	AccountID int64  `json:"account_id"`
	// RequestModel 是客户端请求的模型名（可能是家族名）。
	RequestModel string `json:"request_model"`
	// ResolvedModel 是解析后实际发给上游的模型 ID。
	ResolvedModel string `json:"resolved_model"`
	// UpstreamModel 是上游响应里报告的模型（modelVersion）。
	UpstreamModel string `json:"upstream_model"`
	// Protocol 是入站接口：gemini、anthropic 或 openai。
	Protocol string `json:"protocol"`
	// Reasoning 是推理强度：档位（low/medium/high/xhigh）、客户端的 thinkingLevel，或 "budget:<N>"。
	Reasoning string `json:"reasoning"`
	// PromptTokens 是输入 token 总数，含命中缓存的 CachedTokens。
	PromptTokens   int64  `json:"prompt_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
	CachedTokens   *int64 `json:"cached_tokens"`
	ThoughtsTokens *int64 `json:"thoughts_tokens"`
	TotalTokens    int64  `json:"total_tokens"`
	StatusCode     int    `json:"status_code"`
	Stream         bool   `json:"stream"`
	// FirstTokenMs 是首字延迟：从网关收到请求到读到第一个带内容的流式帧。
	FirstTokenMs *int64 `json:"first_token_ms"`
	LatencyMs    int64  `json:"latency_ms"`
	CreatedAt    int64  `json:"created_at"`
	// ClientIP、UserAgent 是调用方的 IP 和 User-Agent（截断到 512 字符）。
	ClientIP  string `json:"client_ip"`
	UserAgent string `json:"user_agent"`
	// Detail 是 UsageDetail 序列化后的 JSON，老记录为空。
	Detail json.RawMessage `json:"detail,omitempty"`
	// CostNanos 是按模型价格估算的费用（十亿分之一美元）；nil 表示没有算（没有 token，或还没有这个模型的价格）。
	CostNanos *int64 `json:"cost_nanos"`
}

// 入站接口（UsageRecord.Protocol）。
const (
	ProtocolGemini    = "gemini"
	ProtocolAnthropic = "anthropic"
	ProtocolOpenAI    = "openai"
)

// InsertUsage 在一个事务里写入若干条用量记录。CreatedAt 为 0 的记录用当前时间。
func (s *Store) InsertUsage(ctx context.Context, records ...*UsageRecord) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO usage_records
		  (request_id,api_key_id,account_id,request_model,resolved_model,upstream_model,reasoning,
		   prompt_tokens,output_tokens,cached_tokens,thoughts_tokens,total_tokens,status_code,stream,
		   first_token_ms,latency_ms,created_at,client_ip,user_agent,detail_json,cost_nanos,protocol)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := nowMs()
	for _, r := range records {
		stream := 0
		if r.Stream {
			stream = 1
		}
		createdAt := r.CreatedAt
		if createdAt == 0 {
			createdAt = now
		}
		protocol := r.Protocol
		if protocol == "" {
			protocol = ProtocolGemini
		}
		if _, err := stmt.ExecContext(ctx, r.RequestID, r.APIKeyID, r.AccountID, r.RequestModel, r.ResolvedModel,
			r.UpstreamModel, r.Reasoning, r.PromptTokens, r.OutputTokens, r.CachedTokens, r.ThoughtsTokens,
			r.TotalTokens, r.StatusCode, stream, r.FirstTokenMs, r.LatencyMs, createdAt, r.ClientIP, r.UserAgent, string(r.Detail), r.CostNanos, protocol); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// usageCols 是读用量记录的列，顺序与 scanUsage 一致；查询里 usage_records 的别名必须是 u。
const usageCols = `u.id,u.request_id,u.api_key_id,u.account_id,u.request_model,u.resolved_model,u.upstream_model,
	u.reasoning,u.prompt_tokens,u.output_tokens,u.cached_tokens,u.thoughts_tokens,u.total_tokens,u.status_code,
	u.stream,u.first_token_ms,u.latency_ms,u.created_at,u.client_ip,u.user_agent,u.detail_json,u.cost_nanos,u.protocol`

// scanUsage 按 usageCols 的顺序读一行到 r，extra 是查询里跟在后面的附加列。
func scanUsage(row interface{ Scan(...any) error }, r *UsageRecord, extra ...any) error {
	var stream int
	var cached, thoughts, firstToken, cost sql.NullInt64
	var detail string
	dest := []any{&r.ID, &r.RequestID, &r.APIKeyID, &r.AccountID, &r.RequestModel, &r.ResolvedModel, &r.UpstreamModel,
		&r.Reasoning, &r.PromptTokens, &r.OutputTokens, &cached, &thoughts, &r.TotalTokens, &r.StatusCode,
		&stream, &firstToken, &r.LatencyMs, &r.CreatedAt, &r.ClientIP, &r.UserAgent, &detail, &cost, &r.Protocol}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	r.Stream = stream == 1
	r.CachedTokens, r.ThoughtsTokens, r.FirstTokenMs = nullInt(cached), nullInt(thoughts), nullInt(firstToken)
	r.CostNanos = nullInt(cost)
	if detail != "" {
		r.Detail = json.RawMessage(detail)
	}
	return nil
}

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// RecentUsage 返回最近的若干条用量记录。
func (s *Store) RecentUsage(ctx context.Context, limit int) ([]*UsageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+usageCols+` FROM usage_records u ORDER BY u.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UsageRecord
	for rows.Next() {
		var r UsageRecord
		if err := scanUsage(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ModelUsageStat 是按模型聚合的用量。
type ModelUsageStat struct {
	Model        string `json:"model"`
	Requests     int64  `json:"requests"`
	PromptTokens int64  `json:"prompt_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
	// CostNanos 是算过费用的记录的费用合计；一条都没算过时为 nil。
	CostNanos *int64 `json:"cost_nanos"`
}

// UsageByModel 返回 since（Unix 毫秒）以来按请求模型聚合的用量。
func (s *Store) UsageByModel(ctx context.Context, since int64) ([]*ModelUsageStat, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT request_model, COUNT(*), COALESCE(SUM(prompt_tokens),0),
		       COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0), SUM(cost_nanos)
		FROM usage_records WHERE created_at >= ?
		GROUP BY request_model ORDER BY COUNT(*) DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ModelUsageStat
	for rows.Next() {
		var m ModelUsageStat
		var cost sql.NullInt64
		if err := rows.Scan(&m.Model, &m.Requests, &m.PromptTokens, &m.OutputTokens, &m.TotalTokens, &cost); err != nil {
			return nil, err
		}
		m.CostNanos = nullInt(cost)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// HourlyUsage 是一个小时内的请求数和 token 用量。
type HourlyUsage struct {
	Start        int64 `json:"start"` // 这一小时的起点（UTC 整点），Unix 毫秒
	Requests     int64 `json:"requests"`
	Errors       int64 `json:"errors"` // 非 2xx 的请求数
	PromptTokens int64 `json:"prompt_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

const hourMs = 3600_000

// UsageHourly 返回 since（Unix 毫秒）以来按 UTC 整点小时聚合的用量，只含有记录的小时，按时间升序。
func (s *Store) UsageHourly(ctx context.Context, since int64) ([]*HourlyUsage, error) {
	rows, err := s.reader.QueryContext(ctx, `
		SELECT (created_at / ?) * ? AS hour, COUNT(*),
		       COALESCE(SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 0 ELSE 1 END),0),
		       COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0)
		FROM usage_records WHERE created_at >= ?
		GROUP BY hour ORDER BY hour`, hourMs, hourMs, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*HourlyUsage
	for rows.Next() {
		var h HourlyUsage
		if err := rows.Scan(&h.Start, &h.Requests, &h.Errors, &h.PromptTokens, &h.OutputTokens, &h.TotalTokens); err != nil {
			return nil, err
		}
		out = append(out, &h)
	}
	return out, rows.Err()
}

// UsageFilter 是用量记录的查询条件，零值字段不参与过滤。
type UsageFilter struct {
	Since int64 // Unix 毫秒，含
	Until int64 // Unix 毫秒，不含；0 表示不设上限
	// Status 为 "success"（2xx）、"error"（非 2xx），或空。
	Status    string
	Model     string // 请求模型
	AccountID int64
	// APIKeyID 为 nil 时不按 Key 过滤；0 是管理端的账号检测（relay.ProbeKeyID）。
	APIKeyID *int64
	// Stream 为 "stream"、"sync"，或空。
	Stream    string
	RequestID string
	// Protocol 是入站接口（ProtocolGemini、ProtocolAnthropic、ProtocolOpenAI），空为不限。
	Protocol string
}

func (f UsageFilter) where() (string, []any) {
	conds := []string{"u.created_at >= ?"}
	args := []any{f.Since}
	if f.Until > 0 {
		conds = append(conds, "u.created_at < ?")
		args = append(args, f.Until)
	}
	switch f.Status {
	case "success":
		conds = append(conds, "u.status_code BETWEEN 200 AND 299")
	case "error":
		conds = append(conds, "u.status_code NOT BETWEEN 200 AND 299")
	}
	if f.Model != "" {
		conds = append(conds, "u.request_model = ?")
		args = append(args, f.Model)
	}
	if f.Protocol != "" {
		conds = append(conds, "u.protocol = ?")
		args = append(args, f.Protocol)
	}
	if f.AccountID > 0 {
		conds = append(conds, "u.account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.APIKeyID != nil {
		conds = append(conds, "u.api_key_id = ?")
		args = append(args, *f.APIKeyID)
	}
	switch f.Stream {
	case "stream":
		conds = append(conds, "u.stream = 1")
	case "sync":
		conds = append(conds, "u.stream = 0")
	}
	if f.RequestID != "" {
		conds = append(conds, "u.request_id = ?")
		args = append(args, f.RequestID)
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// UsageLog 是带账号邮箱和 API Key 名称的用量记录；账号或 Key 已删除时对应字段为空。
type UsageLog struct {
	UsageRecord
	AccountEmail string `json:"account_email"`
	APIKeyName   string `json:"api_key_name"`
	APIKeyPrefix string `json:"api_key_prefix"`
}

// UsageLogs 按条件分页查用量记录，按 id 倒序，返回当页记录和符合条件的总数。
func (s *Store) UsageLogs(ctx context.Context, f UsageFilter, limit, offset int) ([]*UsageLog, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where, args := f.where()
	var total int64
	if err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM usage_records u`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.reader.QueryContext(ctx, `
		SELECT `+usageCols+`, COALESCE(a.email,''), COALESCE(k.name,''), COALESCE(k.prefix,'')
		FROM usage_records u
		LEFT JOIN accounts a ON a.id = u.account_id
		LEFT JOIN api_keys k ON k.id = u.api_key_id`+where+`
		ORDER BY u.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*UsageLog{}
	for rows.Next() {
		var l UsageLog
		if err := scanUsage(rows, &l.UsageRecord, &l.AccountEmail, &l.APIKeyName, &l.APIKeyPrefix); err != nil {
			return nil, 0, err
		}
		out = append(out, &l)
	}
	return out, total, rows.Err()
}
