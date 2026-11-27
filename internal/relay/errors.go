package relay

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/pool"
)

// Error 是生成失败时交给入站协议层的类型化错误。
//
// Status、Code、Message 可以直接返回给客户端，不含上游地址、账号、额度等内部细节；
// RetryAfter > 0 时应带 Retry-After 头；Err 是内部原因，只进日志。
type Error struct {
	Status     int
	Code       string // Google 风格的错误状态，如 INVALID_ARGUMENT、RESOURCE_EXHAUSTED、UNAVAILABLE
	Message    string
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("relay: %d %s: %v", e.Status, e.Code, e.Err)
	}
	return fmt.Sprintf("relay: %d %s: %s", e.Status, e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

func errModelNotFound(model string) *Error {
	return &Error{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "model not found: " + model, Err: ErrModelNotFound}
}

func errUnavailable(err error) *Error {
	return &Error{Status: http.StatusServiceUnavailable, Code: "UNAVAILABLE", Message: "upstream is temporarily unavailable, retry later", Err: err}
}

// errResponseTooLarge 是上游非流式响应超过大小上限时给客户端的错误：重试也一样，提示改用流式（流式不限大小）。
func errResponseTooLarge(err error) *Error {
	return &Error{Status: http.StatusBadGateway, Code: "UNAVAILABLE",
		Message: "upstream response is too large for a non-streaming request, use streaming instead", Err: err}
}

func errRateLimited(retryAfter time.Duration, err error) *Error {
	return &Error{
		Status:     http.StatusTooManyRequests,
		Code:       "RESOURCE_EXHAUSTED",
		Message:    "all accounts for this model are rate limited or busy, retry later",
		RetryAfter: retryAfter,
		Err:        err,
	}
}

// errRequestRejected 是请求被拒（见 KindRequestRejected）时给客户端的错误：说明是多个账号都拒绝了这个请求，
// 原样重试多半还是失败，让使用者去改请求而不是一直重试。
func errRequestRejected(accounts int, retryAfter time.Duration, err error) *Error {
	return &Error{
		Status: http.StatusTooManyRequests,
		Code:   "RESOURCE_EXHAUSTED",
		Message: fmt.Sprintf("upstream rate-limited this request on %d accounts; the request itself may be rejected, "+
			"retrying it unchanged will likely fail again", accounts),
		RetryAfter: retryAfter,
		Err:        err,
	}
}

// errFromUpstream 把不再换号的上游错误改写成给客户端的错误。
// 403 改写成 503：Claude Code 等客户端收到 403 会以为要重新登录，而这类问题对它来说只是暂时不可用。
// 请求本身的问题（4xx）保留状态码和上游的错误说明，去掉其余细节。
func errFromUpstream(e antigravity.UpstreamError) *Error {
	cause := upstreamCause(e)
	if e.Status == http.StatusForbidden || e.Status >= 500 {
		return errUnavailable(cause)
	}
	code, msg := e.Code, e.Message
	if code == "" {
		code = clientErrorCode(e.Status)
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return &Error{Status: e.Status, Code: code, Message: msg, Err: cause}
}

// upstreamCause 是上游错误的内部描述，只进日志。
func upstreamCause(e antigravity.UpstreamError) error {
	return fmt.Errorf("upstream HTTP %d %s %s: %s", e.Status, e.Kind, e.Reason, e.Message)
}

// StreamFailure 把读流式响应体时遇到的错误换成发给客户端的错误事件：状态码已经发出去了，只能在流里补一个错误事件，
// 客户端据此知道回复不完整，而不是把截断的内容当成正常结束。正常结束和其它错误（如客户端断开）返回 nil。
func StreamFailure(err error) *Error {
	var se *antigravity.StreamError
	switch {
	case errors.Is(err, ErrStreamIdle):
		return &Error{Status: http.StatusGatewayTimeout, Code: "DEADLINE_EXCEEDED", Message: "upstream stream idle timeout", Err: err}
	case errors.As(err, &se):
		return errMidStream(se.UpstreamError)
	}
	return nil
}

// errMidStream 是上游在流式中途推来错误帧时给客户端的错误：和非 2xx 一样不转发上游原文；限流类给 429
// （流已经开始，带不了 Retry-After 头），其余同 errFromUpstream。
func errMidStream(e antigravity.UpstreamError) *Error {
	if (&failure{kind: e.Kind}).rateLimited() {
		return &Error{Status: http.StatusTooManyRequests, Code: "RESOURCE_EXHAUSTED",
			Message: "upstream rate limit hit mid-stream, retry later", Err: upstreamCause(e)}
	}
	return errFromUpstream(e)
}

func clientErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusRequestEntityTooLarge:
		return "INVALID_ARGUMENT"
	default:
		return "FAILED_PRECONDITION"
	}
}

// failure 记录一次换号重试前的失败，重试用完或没号可选时据此决定返回什么。
type failure struct {
	kind     antigravity.ErrorKind // 传输失败、取令牌失败时为空
	err      error
	cooldown time.Duration // 这次失败让账号冷却了多久，0 表示没冷却
}

// rateLimited 报告失败是否属于限流类（额度、限流、容量）。
func (f *failure) rateLimited() bool {
	if f == nil {
		return false
	}
	switch f.kind {
	case antigravity.KindQuotaExhausted, antigravity.KindRateLimit, antigravity.KindModelCapacity:
		return true
	}
	return false
}

// exhaustedError 在没有账号可再试时（号池不可用，或尝试次数用完）给出返回给客户端的错误。
// poolErr 是最后一次选号的错误，可为 nil（尝试次数用完）；last 是最近一次上游失败，可为 nil。
// minCooldown 是本次请求里施加过的最短冷却，用作 Retry-After 的兜底。
func exhaustedError(poolErr error, last *failure, minCooldown time.Duration) *Error {
	var ue *pool.UnavailableError
	switch {
	case errors.As(poolErr, &ue) && !ue.RetryAt.IsZero():
		return errRateLimited(retryAfter(time.Until(ue.RetryAt)), poolErr)
	case errors.As(poolErr, &ue) && ue.Busy:
		return errRateLimited(time.Second, poolErr)
	case last.rateLimited():
		return errRateLimited(retryAfter(minCooldown), last.err)
	case last != nil:
		return errUnavailable(last.err)
	case poolErr != nil:
		return errUnavailable(poolErr)
	default:
		return errUnavailable(errors.New("exhausted account retries"))
	}
}

// retryAfter 把时长向上取到秒，至少 1 秒。
func retryAfter(d time.Duration) time.Duration {
	if d < time.Second {
		return time.Second
	}
	if r := d % time.Second; r != 0 {
		d += time.Second - r
	}
	return d
}
