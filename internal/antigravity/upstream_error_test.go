package antigravity

import (
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestClassifyError(t *testing.T) {
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		status int
		header http.Header
		body   string
		kind   ErrorKind
		reset  time.Duration // 期望 ResetAt - now，0 表示没有
	}{
		{"401", 401, nil, `{"error":{"code":401}}`, KindUnauthorized, 0},
		{"403 条款封禁（文本）", 403, nil,
			`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Gemini has been disabled in this account for violation of Terms of Service."}}`,
			KindTOSViolation, 0},
		{"403 条款封禁（reason）", 403, nil,
			`{"error":{"code":403,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION"}]}}`,
			KindTOSViolation, 0},
		{"403 需要验证", 403, nil,
			`{"error":{"code":403,"message":"Please verify your account","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED","metadata":{"validation_url":"https://x"}}]}}`,
			KindValidationRequired, 0},
		{"403 无权使用模型", 403, nil, `{"error":{"code":403,"message":"The caller does not have access to model claude-opus-5-5"}}`, KindModelForbidden, 0},
		{"403 地区限制按原因不明", 403, nil, `{"error":{"code":403,"message":"User location is not supported for the API use."}}`, KindForbidden, 0},
		{"403 订阅校验按原因不明", 403, nil, `{"error":{"code":403,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SUBSCRIPTION_REQUIRED"}]}}`, KindForbidden, 0},
		{"429 额度耗尽带绝对时间", 429, nil,
			`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"1h","quotaResetTimeStamp":"2026-11-30T08:00:00Z"}},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"60s"}]}}`,
			KindQuotaExhausted, 7 * 24 * time.Hour},
		{"429 额度耗尽带相对时长", 429, nil,
			`{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"154h21m14s"}}]}}`,
			KindQuotaExhausted, 154*time.Hour + 21*time.Minute + 14*time.Second},
		{"429 限流但重置时间长，按额度耗尽", 429, nil,
			`{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"600s"}]}}`,
			KindQuotaExhausted, 10 * time.Minute},
		{"429 瞬时限流带短时间", 429, nil,
			`{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2.5s"}]}}`,
			KindRateLimit, 2500 * time.Millisecond},
		{"429 无法识别按瞬时限流", 429, nil, `{"error":{"code":429,"message":"too many"}}`, KindRateLimit, 0},
		{"429 正文和 Retry-After 取较晚", 429, http.Header{"Retry-After": {"120"}},
			`{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`,
			KindRateLimit, 2 * time.Minute},
		{"429 Retry-After 为日期", 429, http.Header{"Retry-After": {"Mon, 23 Nov 2026 08:05:00 GMT"}}, `{}`, KindRateLimit, 5 * time.Minute},
		{"429 Retry-After 已过期不算", 429, http.Header{"Retry-After": {"Mon, 23 Nov 2026 07:00:00 GMT"}}, `{}`, KindRateLimit, 0},
		{"429 模型容量不足", 429, nil,
			`{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`,
			KindModelCapacity, 0},
		{"503 模型容量不足", 503, nil, `{"error":{"code":503,"message":"MODEL_CAPACITY_EXHAUSTED"}}`, KindModelCapacity, 0},
		{"503 普通按服务端错误", 503, nil, `{"error":{"code":503,"status":"UNAVAILABLE"}}`, KindServerError, 0},
		{"500", 500, nil, `{}`, KindServerError, 0},
		{"408", 408, nil, `{}`, KindServerError, 0},
		{"400", 400, nil, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"bad"}}`, KindClientError, 0},
		{"404", 404, nil, `{"error":{"code":404,"status":"NOT_FOUND"}}`, KindClientError, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := ClassifyError(tc.status, tc.header, []byte(tc.body), now)
			if e.Kind != tc.kind || e.Status != tc.status {
				t.Fatalf("分类应为 %s，实际 %s（%+v）", tc.kind, e.Kind, e)
			}
			var got time.Duration
			if !e.ResetAt.IsZero() {
				got = e.ResetAt.Sub(now)
			}
			if got != tc.reset {
				t.Fatalf("恢复时间应在 %v 后，实际 %v", tc.reset, got)
			}
		})
	}
}

// 4xx 的说明原样保留给客户端，超长截断；error.status 读进 Code。
func TestClassifyErrorMessage(t *testing.T) {
	e := ClassifyError(400, nil, []byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"contents is required"}}`), time.Now())
	if e.Message != "contents is required" || e.Code != "INVALID_ARGUMENT" {
		t.Fatalf("应读出 message 和 status：%+v", e)
	}
	long := make([]byte, 0, 3000)
	for i := 0; i < 1500; i++ {
		long = append(long, "长"...)
	}
	e = ClassifyError(400, nil, []byte(`{"error":{"message":"`+string(long)+`"}}`), time.Now())
	if n := len([]rune(e.Message)); n != messageLimit+1 {
		t.Fatalf("说明应截断到 %d 字加省略号，实际 %d", messageLimit, n)
	}
}

func TestIsInvalidGrant(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&HTTPError{Status: 400, Body: `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`}, true},
		{&HTTPError{Status: 400, Body: `{"error":"invalid_request"}`}, false},
		{&HTTPError{Status: 401, Body: `{"error":"unauthorized_client"}`}, false},
		{&HTTPError{Status: 500, Body: `oops`}, false},
		{http.ErrHandlerTimeout, false},
	}
	for _, tc := range cases {
		if got := IsInvalidGrant(tc.err); got != tc.want {
			t.Fatalf("%v：应为 %v，实际 %v", tc.err, tc.want, got)
		}
	}
}

// 上游错误原文：短的原样保留；超过 rawLimit 的截断，截断处不留半个 UTF-8 字符，末尾加省略号。
func TestClassifyErrorKeepsRaw(t *testing.T) {
	body := []byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`)
	if e := ClassifyError(429, nil, body, time.Now()); e.Raw != string(body) {
		t.Fatalf("短的应原样保留：%q", e.Raw)
	}
	long := []byte(`{"error":{"code":400,"message":"` + strings.Repeat("超", rawLimit) + `"}}`)
	e := ClassifyError(400, nil, long, time.Now())
	if !strings.HasSuffix(e.Raw, "…") || len(e.Raw) > rawLimit+len("…") || !utf8.ValidString(e.Raw) {
		t.Fatalf("长的应截到 %d 字节内、不留半个字符：len=%d valid=%v", rawLimit, len(e.Raw), utf8.ValidString(e.Raw))
	}
}
