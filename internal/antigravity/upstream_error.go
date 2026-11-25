package antigravity

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// ErrorKind 是上游非 2xx 响应的分类。账号怎么处理、请求要不要换号由 relay 按分类决定。
type ErrorKind string

const (
	KindUnauthorized       ErrorKind = "unauthorized"        // 401：access token 失效
	KindTOSViolation       ErrorKind = "tos_violation"       // 403：条款封禁，账号被永久停用
	KindValidationRequired ErrorKind = "validation_required" // 403：账号需要额外验证，临时性的
	KindModelForbidden     ErrorKind = "model_forbidden"     // 403：账号无权使用该模型
	KindForbidden          ErrorKind = "forbidden"           // 403：原因不明（地区限制、订阅校验等）
	KindQuotaExhausted     ErrorKind = "quota_exhausted"     // 429：额度窗口耗尽
	KindRateLimit          ErrorKind = "rate_limit"          // 429：瞬时限流
	KindModelCapacity      ErrorKind = "model_capacity"      // 429/503：模型全局容量不足
	KindServerError        ErrorKind = "server_error"        // 5xx、408
	KindClientError        ErrorKind = "client_error"        // 其它 4xx：请求本身的问题
)

// UpstreamError 是对一个上游非 2xx 响应的分类结果。
type UpstreamError struct {
	Status int
	Kind   ErrorKind
	// Reason 是 ErrorInfo.reason（如 TOS_VIOLATION、QUOTA_EXHAUSTED），没有时为空。
	Reason string
	// Code 是 error.status（如 INVALID_ARGUMENT、RESOURCE_EXHAUSTED）。
	Code string
	// Message 是 error.message，截断到 messageLimit 个字符。
	Message string
	// ResetAt 是上游给出的恢复时间：正文（quotaResetTimeStamp、quotaResetDelay、retryDelay）和
	// Retry-After 头取较晚者；零值表示上游没给，或给的时间已过。
	ResetAt time.Time
}

// StreamError 是上游在流式响应中途推来的错误帧：HTTP 状态已经是 200，错误以 {"error":{...}} 一帧给出。
// 分类同非 2xx 响应（见 inBandError）。读流式响应体时作为 error 返回，之后的读取都返回它。
type StreamError struct {
	UpstreamError
}

func (e *StreamError) Error() string {
	return fmt.Sprintf("antigravity: upstream error frame in stream: HTTP %d %s %s: %s", e.Status, e.Kind, e.Reason, e.Message)
}

// inBandError 识别上游放在 2xx 响应里的错误（{"error":{...}}：流式是其中一帧，非流式是整个正文），
// 按 error.code 当状态码分类，code 不是 4xx、5xx 时按 502 算。上游有时先回 200，
// 再把 429 等错误写进正文。
func inBandError(raw []byte, header http.Header, now time.Time) (UpstreamError, bool) {
	e := gjson.GetBytes(raw, "error")
	if !e.IsObject() {
		return UpstreamError{}, false
	}
	status := int(e.Get("code").Int())
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	return ClassifyError(status, header, raw, now), true
}

const (
	messageLimit = 1000
	// RATE_LIMIT_EXCEEDED 的重置时间超过这个长度时，实际是额度窗口耗尽而不是瞬时限流。
	quotaExhaustedThreshold = 5 * time.Minute
)

// 403 的判定文本，匹配前把响应体转小写并合并空白。
// 只有明确的条款封禁才会让账号停用，其余 403 不能当成账号已死。
var (
	tosMarkers = []string{
		"violation of terms of service",
		"tos_violation",
		"service has been disabled in this account",
		"submit an appeal",
	}
	validationMarkers = []string{
		"validation_required",
		"verify your account",
		"validation_url",
		"requires verification",
	}
	modelForbiddenMarkers = []string{
		"model is not available",
		"model is not permitted",
		"model access",
		"does not have access to model",
		"not allowed to use model",
	}
)

// ClassifyError 分类一个上游非 2xx 响应。header 可为 nil；now 用来把相对时长换成绝对时间。
func ClassifyError(status int, header http.Header, body []byte, now time.Time) UpstreamError {
	e := UpstreamError{
		Status:  status,
		Code:    strings.ToUpper(strings.TrimSpace(gjson.GetBytes(body, "error.status").String())),
		Message: truncateRunes(strings.TrimSpace(gjson.GetBytes(body, "error.message").String()), messageLimit),
	}
	bodyReset, hasDelay := parseErrorDetails(body, now, &e)
	e.ResetAt = laterFuture(now, bodyReset, parseRetryAfter(header, now))
	text := strings.Join(strings.Fields(strings.ToLower(string(body))), " ")

	switch {
	case status == http.StatusUnauthorized:
		e.Kind = KindUnauthorized
	case status == http.StatusForbidden:
		e.Kind = classify403(e.Reason, text)
	case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
		e.Kind = classifyQuota(status, e, text, hasDelay, now)
	case status == http.StatusRequestTimeout || status >= 500:
		e.Kind = KindServerError
	default:
		e.Kind = KindClientError
	}
	return e
}

func classify403(reason, text string) ErrorKind {
	switch {
	case reason == "TOS_VIOLATION" || containsAny(text, tosMarkers):
		return KindTOSViolation
	case reason == "VALIDATION_REQUIRED" || containsAny(text, validationMarkers):
		return KindValidationRequired
	case containsAny(text, modelForbiddenMarkers) ||
		(strings.Contains(text, "model ") && (strings.Contains(text, "not available") || strings.Contains(text, "not permitted"))):
		return KindModelForbidden
	default:
		return KindForbidden
	}
}

// classifyQuota 分类 429 和 503。识别不出额度信号的 429 按瞬时限流算，503 按服务端错误算。
func classifyQuota(status int, e UpstreamError, text string, hasDelay bool, now time.Time) ErrorKind {
	switch e.Reason {
	case "MODEL_CAPACITY_EXHAUSTED":
		return KindModelCapacity
	case "QUOTA_EXHAUSTED":
		return KindQuotaExhausted
	case "RATE_LIMIT_EXCEEDED":
		if hasDelay && e.ResetAt.Sub(now) >= quotaExhaustedThreshold {
			return KindQuotaExhausted
		}
		return KindRateLimit
	}
	switch {
	case strings.Contains(text, "model_capacity_exhausted"):
		return KindModelCapacity
	case strings.Contains(text, "quota_exhausted") || strings.Contains(text, "quota exhausted"):
		return KindQuotaExhausted
	case status == http.StatusTooManyRequests:
		return KindRateLimit
	default:
		return KindServerError
	}
}

// parseErrorDetails 读取 error.details 里的 ErrorInfo（reason、恢复时间）和 RetryInfo。
// 恢复时间的优先级：quotaResetTimeStamp（绝对时间）> quotaResetDelay > retryDelay。
func parseErrorDetails(body []byte, now time.Time, e *UpstreamError) (reset time.Time, hasDelay bool) {
	var stamp, delay, retry time.Time
	for _, d := range gjson.GetBytes(body, "error.details").Array() {
		typeName := d.Get("@type").String()
		switch {
		case strings.HasSuffix(typeName, "google.rpc.ErrorInfo"):
			if r := strings.ToUpper(strings.TrimSpace(d.Get("reason").String())); r != "" && e.Reason == "" {
				e.Reason = r
			}
			if s := d.Get("metadata.quotaResetTimeStamp").String(); s != "" && stamp.IsZero() {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					stamp = t
				}
			}
			if v, ok := parseRetryDelay(d.Get("metadata.quotaResetDelay")); ok && delay.IsZero() {
				delay = now.Add(v)
			}
		case strings.HasSuffix(typeName, "google.rpc.RetryInfo"):
			if v, ok := parseRetryDelay(d.Get("retryDelay")); ok && retry.IsZero() {
				retry = now.Add(v)
			}
		}
	}
	for _, t := range []time.Time{stamp, delay, retry} {
		if !t.IsZero() {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseRetryAfter 解析 Retry-After 头：非负整数秒，或 HTTP 日期。
func parseRetryAfter(h http.Header, now time.Time) time.Time {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return time.Time{}
	}
	if d, err := time.ParseDuration(v + "s"); err == nil && d >= 0 {
		return now.Add(d)
	}
	if t, err := http.ParseTime(v); err == nil {
		return t
	}
	return time.Time{}
}

func parseRetryDelay(v gjson.Result) (time.Duration, bool) {
	if !v.Exists() {
		return 0, false
	}
	switch v.Type {
	case gjson.String:
		s := strings.TrimSpace(v.String())
		if s == "" {
			return 0, false
		}
		if d, err := time.ParseDuration(s); err == nil && d >= 0 {
			return d, true
		}
	case gjson.Number:
		if sec := v.Float(); sec >= 0 {
			return time.Duration(sec * float64(time.Second)), true
		}
	case gjson.JSON:
		sec := v.Get("seconds")
		nanos := v.Get("nanos")
		if !sec.Exists() && !nanos.Exists() {
			return 0, false
		}
		d := time.Duration(sec.Float()*float64(time.Second)) + time.Duration(nanos.Int())
		if d >= 0 {
			return d, true
		}
	}
	return 0, false
}

// laterFuture 返回 a、b 中较晚且晚于 now 的时间，都不满足时返回零值。
func laterFuture(now, a, b time.Time) time.Time {
	if b.After(a) {
		a = b
	}
	if a.After(now) {
		return a
	}
	return time.Time{}
}

func containsAny(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
