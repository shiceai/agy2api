package relay

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/pool"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	p := DefaultPolicy()
	cases := []struct {
		name string
		err  antigravity.UpstreamError
		want decision
	}{
		{"条款封禁：停用，不换号", antigravity.UpstreamError{Kind: antigravity.KindTOSViolation},
			decision{action: actionReturn, disable: ReasonTOSViolation}},
		{"需要验证：冷却，不换号", antigravity.UpstreamError{Kind: antigravity.KindValidationRequired},
			decision{action: actionReturn, cooldown: p.ValidationCooldown}},
		{"无权使用模型：冷却，不换号", antigravity.UpstreamError{Kind: antigravity.KindModelForbidden},
			decision{action: actionReturn, cooldown: p.ModelForbiddenCooldown}},
		{"原因不明的 403：不动账号，不换号", antigravity.UpstreamError{Kind: antigravity.KindForbidden},
			decision{action: actionReturn}},
		{"额度耗尽带恢复时间：冷却到恢复，换号", antigravity.UpstreamError{Kind: antigravity.KindQuotaExhausted, ResetAt: now.Add(6 * time.Hour)},
			decision{action: actionRetry, cooldown: 6 * time.Hour}},
		{"恢复时间离谱：封顶 8 天", antigravity.UpstreamError{Kind: antigravity.KindQuotaExhausted, ResetAt: now.Add(30 * 24 * time.Hour)},
			decision{action: actionRetry, cooldown: maxResetCooldown}},
		{"恢复时间不足 1 秒：按 1 秒", antigravity.UpstreamError{Kind: antigravity.KindRateLimit, ResetAt: now.Add(100 * time.Millisecond)},
			decision{action: actionRetry, cooldown: time.Second}},
		{"瞬时限流不带时间：连续退避，换号", antigravity.UpstreamError{Kind: antigravity.KindRateLimit},
			decision{action: actionRetry, backoff: true}},
		{"额度耗尽不带时间：也按连续退避", antigravity.UpstreamError{Kind: antigravity.KindQuotaExhausted},
			decision{action: actionRetry, backoff: true}},
		{"容量不足：短暂退避，换号", antigravity.UpstreamError{Kind: antigravity.KindModelCapacity},
			decision{action: actionRetry, cooldown: capacityCooldown}},
		{"5xx：休息，换号", antigravity.UpstreamError{Kind: antigravity.KindServerError},
			decision{action: actionRetry, cooldown: p.ServerErrorCooldown}},
		{"刷新后仍 401：换号，不冷却", antigravity.UpstreamError{Kind: antigravity.KindUnauthorized},
			decision{action: actionRetry}},
		{"请求本身的错误：原样返回", antigravity.UpstreamError{Kind: antigravity.KindClientError},
			decision{action: actionReturn}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decide(tc.err, p, now); got != tc.want {
				t.Fatalf("期望 %+v，实际 %+v", tc.want, got)
			}
		})
	}

	// 5xx 休息时间设成 0：不冷却。
	p.ServerErrorCooldown = 0
	if got := decide(antigravity.UpstreamError{Kind: antigravity.KindServerError}, p, now); got.cooldown != 0 || got.action != actionRetry {
		t.Fatalf("5xx 休息为 0 时不应冷却：%+v", got)
	}
}

func TestRateLimitBackoff(t *testing.T) {
	p := DefaultPolicy() // 60 秒起，最多 300 秒
	want := []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second, 300 * time.Second, 300 * time.Second}
	for i, w := range want {
		if got := rateLimitBackoff(i+1, p); got != w {
			t.Fatalf("连续第 %d 次应冷却 %v，实际 %v", i+1, w, got)
		}
	}
}

func TestErrFromUpstream(t *testing.T) {
	cases := []struct {
		name         string
		err          antigravity.UpstreamError
		status       int
		code, msgHas string
	}{
		{"403 改写成 503，不带上游说明",
			antigravity.UpstreamError{Status: 403, Kind: antigravity.KindTOSViolation, Message: "disabled for violation of Terms of Service"},
			503, "UNAVAILABLE", "temporarily unavailable"},
		{"400 保留状态和说明",
			antigravity.UpstreamError{Status: 400, Kind: antigravity.KindClientError, Code: "INVALID_ARGUMENT", Message: "contents is required"},
			400, "INVALID_ARGUMENT", "contents is required"},
		{"404 没有 status 时补上",
			antigravity.UpstreamError{Status: 404, Kind: antigravity.KindClientError},
			404, "NOT_FOUND", "Not Found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := errFromUpstream(tc.err)
			if e.Status != tc.status || e.Code != tc.code || !strings.Contains(e.Message, tc.msgHas) {
				t.Fatalf("期望 %d %s %q，实际 %+v", tc.status, tc.code, tc.msgHas, e)
			}
			if tc.status == 503 && strings.Contains(e.Message, "Terms") {
				t.Fatalf("不应暴露上游说明：%q", e.Message)
			}
		})
	}
}

func TestExhaustedError(t *testing.T) {
	rateLimited := &failure{kind: antigravity.KindRateLimit, err: errors.New("429"), cooldown: 90 * time.Second}
	serverError := &failure{kind: antigravity.KindServerError, err: errors.New("500")}
	cases := []struct {
		name       string
		poolErr    error
		last       *failure
		status     int
		retryAfter time.Duration // 0 表示不带
	}{
		{"都在冷却：429，按最早恢复", &pool.UnavailableError{RetryAt: time.Now().Add(42 * time.Second)}, nil, 429, 42 * time.Second},
		{"并发都满：429，1 秒后重试", &pool.UnavailableError{Busy: true}, nil, 429, time.Second},
		{"都试过且最后是限流：429，按施加的冷却", &pool.UnavailableError{}, rateLimited, 429, 90 * time.Second},
		{"尝试次数用完且最后是 5xx：503", nil, serverError, 503, 0},
		{"没有任何账号：503", pool.ErrNoAccount, nil, 503, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := exhaustedError(tc.poolErr, tc.last, 90*time.Second)
			if e.Status != tc.status {
				t.Fatalf("状态应为 %d，实际 %d", tc.status, e.Status)
			}
			if tc.retryAfter == 0 && e.RetryAfter != 0 {
				t.Fatalf("不应带 Retry-After，实际 %v", e.RetryAfter)
			}
			if tc.retryAfter != 0 && (e.RetryAfter < tc.retryAfter-time.Second || e.RetryAfter > tc.retryAfter+time.Second) {
				t.Fatalf("Retry-After 应约为 %v，实际 %v", tc.retryAfter, e.RetryAfter)
			}
			if e.Status == http.StatusTooManyRequests && e.Code != "RESOURCE_EXHAUSTED" {
				t.Fatalf("429 的 code 应为 RESOURCE_EXHAUSTED：%s", e.Code)
			}
		})
	}
	if d := retryAfter(1500 * time.Millisecond); d != 2*time.Second {
		t.Fatalf("Retry-After 应向上取整到秒：%v", d)
	}
}
