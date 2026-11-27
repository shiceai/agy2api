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
	// Request 是发给上游的请求的结构摘要（不含正文），只给上游返回了错误的请求记，排查请求为什么被拒。
	Request *UsageRequestSummary `json:"request,omitempty"`
	// RequestBody 是这次失败请求的完整请求体有没有存下（开了"保存失败请求的正文"时）：saved 已交给后台保存，
	// skipped 因限速或排队满没存；没开时为空。正文在 request_bodies 表里，按请求 ID 取。
	RequestBody string `json:"request_body,omitempty"`
}

// 失败请求正文的保存结果（UsageDetail.RequestBody）。
const (
	RequestBodySaved   = "saved"
	RequestBodySkipped = "skipped"
)

// UsageRequestSummary 是发给上游的 Gemini 请求的结构摘要：只有计数和大小，不含文字内容。
type UsageRequestSummary struct {
	Bytes int `json:"bytes"` // 发给上游的请求体（含信封）字节数
	// Turns 是 contents 的条数，Roles 是按角色（user、model）的条数。
	Turns int            `json:"turns"`
	Roles map[string]int `json:"roles,omitempty"`
	// TextChars 是 contents 里文本 part 的总字符数（含思考），SystemChars 是 systemInstruction 的。
	TextChars   int `json:"text_chars"`
	SystemChars int `json:"system_chars"`
	// Media 是内联数据和文件引用，按 MIME 类型汇总。
	Media             []UsageMedia `json:"media,omitempty"`
	FunctionCalls     int          `json:"function_calls,omitempty"`
	FunctionResponses int          `json:"function_responses,omitempty"`
	// Tools 是函数声明个数，OtherTools 是其它工具条目（如 googleSearch）的名字。
	Tools      int      `json:"tools,omitempty"`
	OtherTools []string `json:"other_tools,omitempty"`
	// Generation 是 generationConfig 里的关键参数（输出上限、思考设置、温度等），结构化输出只记有没有。
	Generation map[string]any `json:"generation,omitempty"`
	// Safety 是 safetySettings，写成"类别=阈值"。
	Safety []string `json:"safety,omitempty"`
}

// UsageMedia 是一种 MIME 类型的内联数据（Bytes 为解码后的字节数）或文件引用（Files 个数）。
type UsageMedia struct {
	MimeType string `json:"mime_type"`
	Count    int    `json:"count"`
	Bytes    int    `json:"bytes,omitempty"`
	Files    int    `json:"files,omitempty"`
}

// UsageAffinity 是会话粘性的结果：Result 为 hit（沿用上次的账号）、switched（换了账号）、new（新对话）。
// 换号时 Reason 是上次的账号这次不能接的原因（见 pool.Affinity*），FromAccountID、FromAccount 是上次的账号。
// Source 是按什么认的对话（Session*），老记录为空（当时都按首条用户消息）。
type UsageAffinity struct {
	Result        string `json:"result"`
	Reason        string `json:"reason,omitempty"`
	FromAccountID int64  `json:"from_account_id,omitempty"`
	FromAccount   string `json:"from_account,omitempty"`
	Source        string `json:"source,omitempty"`
}

// 会话粘性认对话的依据（UsageAffinity.Source）：客户端给的会话标识，没有时按首条用户消息。
const (
	SessionClaudeCode      = "claude_code"       // 请求头 X-Claude-Code-Session-Id
	SessionClaudeCodeAgent = "claude_code_agent" // 同上，另有 X-Claude-Code-Agent-Id（子代理）
	SessionHeader          = "header"            // 请求头 X-Session-Id
	SessionPromptCacheKey  = "prompt_cache_key"  // OpenAI 请求体的 prompt_cache_key
	SessionFirstMessage    = "first_message"     // 首条用户消息
)

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
	// Raw 是上游错误响应的原文（截到 4 KB），排查上游为什么拒绝时看。
	Raw string `json:"raw,omitempty"`
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
