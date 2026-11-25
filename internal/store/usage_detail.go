package store

import (
	"context"
	"database/sql"
	"errors"
)

// UsageDetail 是用量记录的详情，序列化后存在 usage_records.detail_json，只在管理端的耗时悬浮和详情弹窗里用。
type UsageDetail struct {
	Timings *UsageTimings `json:"timings,omitempty"`
	// Slot 是选中账号时它的并发占用（含本次）和上限。
	Slot     *UsageSlot     `json:"slot,omitempty"`
	Attempts []UsageAttempt `json:"attempts,omitempty"`
	// Error 是最终失败的原因，成功时为空。
	Error *UsageError `json:"error,omitempty"`
	// ResponseID 是上游响应里的 responseId；TraceID 是上游的 trace ID（响应头 x-cloudaicompanion-trace-id
	// 或信封里的 traceId），向上游排查时用。
	ResponseID string `json:"response_id,omitempty"`
	TraceID    string `json:"trace_id,omitempty"`
	// FakeStream 表示客户端请求的是假流式（模型名带 假流式/ 前缀）：上游用非流式请求，结果按流式返回。
	FakeStream bool `json:"fake_stream,omitempty"`
	// Cost 是费用的算法（按哪个价格、用了什么单价），没有算费用时为空。
	Cost *UsageCost `json:"cost,omitempty"`
	// Affinity 是会话粘性的结果，没开粘性或请求里没有用户消息时为空。
	Affinity *UsageAffinity `json:"affinity,omitempty"`
}

// UsageAffinity 是会话粘性的结果：Result 为 hit（沿用上次的账号）、switched（换了账号）、new（新对话）。
// 换号时 Reason 是上次的账号这次不能接的原因（见 pool.Affinity*），FromAccountID、FromAccount 是上次的账号。
type UsageAffinity struct {
	Result        string `json:"result"`
	Reason        string `json:"reason,omitempty"`
	FromAccountID int64  `json:"from_account_id,omitempty"`
	FromAccount   string `json:"from_account,omitempty"`
}

// UsageCost 是一条用量的费用和算法。单价是美元每百万 token，按百万分之一美元存整数。
type UsageCost struct {
	Nanos  int64  `json:"nanos"`  // 费用，十亿分之一美元，与 usage_records.cost_nanos 相同
	Model  string `json:"model"`  // 价格对应的型号（官方型号 ID，或手动价的模型名）
	Source string `json:"source"` // official / manual
	Input  int64  `json:"input"`
	// CacheRead 是命中缓存的输入的单价；官方没有单独的缓存价时与 Input 相同。
	CacheRead int64 `json:"cache_read"`
	Output    int64 `json:"output"`          // 输出和思考的单价
	Tier      bool  `json:"tier,omitempty"`  // 提示超过分档阈值，用了长提示的价格
	Promo     bool  `json:"promo,omitempty"` // 用了限时价
	// Backfilled 表示记录时还没有这个模型的价格，之后按当时有效的价格补算。
	Backfilled bool `json:"backfilled,omitempty"`
}

// UsageTimings 是最终那次尝试的耗时明细，单位毫秒。AcquireMs、TokenMs 是时长；其余是从收到请求起算的时间点
// （首字、总耗时在 UsageRecord 里）。nil 表示没有这项（如非流式没有首个上游事件）。
type UsageTimings struct {
	// CooldownWaitMs 是等冷却的累计时长：账号都在冷却、最早的很快恢复时先等再试（整个请求累计，不含在 AcquireMs 里）。
	CooldownWaitMs *int64 `json:"cooldown_wait_ms,omitempty"`
	AcquireMs      *int64 `json:"acquire_ms,omitempty"`     // 选号（含等并发名额）用时
	TokenMs        *int64 `json:"token_ms,omitempty"`       // 取 access token 用时（需要刷新时会慢）
	HeaderMs       *int64 `json:"header_ms,omitempty"`      // 上游返回响应头
	FirstEventMs   *int64 `json:"first_event_ms,omitempty"` // 流式第一个上游帧（含不带内容的帧）
	FirstTextMs    *int64 `json:"first_text_ms,omitempty"`  // 流式第一个正文：非思考的文本或工具调用
	// UpstreamMs 是上游在响应头 Server-Timing 里自报的处理时长（时长，不是时间点）：非流式约等于整个生成，
	// 流式接近首字。
	UpstreamMs *int64 `json:"upstream_ms,omitempty"`
}

// UsageSlot 是账号的并发占用快照。
type UsageSlot struct {
	InUse int `json:"in_use"`
	Limit int `json:"limit"`
}

// UsageAttempt 是一次换号尝试。
type UsageAttempt struct {
	Try       int    `json:"try"`
	AccountID int64  `json:"account_id"`
	Account   string `json:"account"` // 尝试时的账号邮箱快照
	Model     string `json:"model"`   // 实际发给上游的模型
	// Status 是上游状态码；连接失败、取令牌失败时为 0。
	Status int `json:"status,omitempty"`
	// Result 是 success，或上游错误的分类（rate_limit、server_error 等），或 transport（连接、取令牌失败）。
	Result     string `json:"result"`
	Error      string `json:"error,omitempty"`
	CooldownMs int64  `json:"cooldown_ms,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// UsageError 是请求最终失败的原因。
type UsageError struct {
	Kind    string `json:"kind"`              // 上游错误分类，或 transport
	Reason  string `json:"reason,omitempty"`  // 上游 ErrorInfo.reason
	Message string `json:"message,omitempty"` // 上游 error.message 或内部原因
}

// UsageLogByID 按 ID 读一条带账号邮箱和 API Key 名称的用量记录。
func (s *Store) UsageLogByID(ctx context.Context, id int64) (*UsageLog, error) {
	row := s.reader.QueryRowContext(ctx, `
		SELECT `+usageCols+`, COALESCE(a.email,''), COALESCE(k.name,''), COALESCE(k.prefix,'')
		FROM usage_records u
		LEFT JOIN accounts a ON a.id = u.account_id
		LEFT JOIN api_keys k ON k.id = u.api_key_id
		WHERE u.id = ?`, id)
	var l UsageLog
	err := scanUsage(row, &l.UsageRecord, &l.AccountEmail, &l.APIKeyName, &l.APIKeyPrefix)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}
