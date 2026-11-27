package relay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/store"
)

// 没拿到上游响应就失败的请求也记一条用量，用量页能看到客户端为什么失败。拿到了上游响应的失败由 recordFailure 记。

// 这类失败在用量详情里的原因分类（上游错误的分类见 antigravity.ErrorKind）。
const (
	KindNoAccount       = "no_account"        // 没有可用账号（都在冷却、并发满了等不到名额）
	KindModelNotFound   = "model_not_found"   // 没有账号的目录能解析出请求的模型
	KindCanceled        = "canceled"          // 客户端在拿到结果前取消
	KindTransport       = "transport"         // 尝试过的账号都连不上上游（或取不到令牌）
	KindProxy           = "proxy"             // 最后尝试的账号连不上它绑定的代理（请求没到上游）
	KindKeyQuota        = "key_quota"         // API Key 的 Token 额度用完
	KindModelNotAllowed = "model_not_allowed" // API Key 不允许请求这个模型
	KindClientFiltered  = "client_filtered"   // system 提示词命中客户端名称过滤的拦截规则
	KindRemoteImage     = "remote_image"      // 远程图片 URL 下载/内联失败
	// KindResponseTooLarge：上游非流式响应超过大小上限（antigravity.ErrResponseTooLarge），生成完成了但结果给不了客户端。
	KindResponseTooLarge = "response_too_large"
	// KindRequestRejected：rejectedAccounts 个账号都对同一请求回了不带原因、不带恢复时间的 429，按请求本身被上游拒绝处理
	// （同时段别的请求在这些账号上照常成功）。也用作这时缩短后的冷却原因。
	KindRequestRejected = "request_rejected"
)

// 请求被拒的判定：同一请求在 rejectedAccounts 个不同账号上都遇到不带原因、不带恢复时间的 429，就不再换号，
// 这些账号因这次请求进入的冷却缩短到 rejectedCooldown、也不计入连续限流（不让之后的冷却翻倍）。一条上游不接受的
// 请求不应让这个模型在所有账号上停一两分钟、误伤别人；真的是所有账号同时被限流时，短冷却仍能挡住连打。
const (
	rejectedAccounts = 2
	rejectedCooldown = 10 * time.Second
)

// proxyPause 是账号的代理连不上时暂停调度的时长：代理恢复后很快能重新用上，也不至于每个请求都先撞一次。
const proxyPause = time.Minute

// StatusClientClosed 是客户端取消请求时记的状态码（nginx 的约定），客户端实际上收不到它。
const StatusClientClosed = 499

// clientUA 把调用方的 User-Agent 截到能记进用量的长度。
func clientUA(ua string) string {
	if len(ua) > userAgentLimit {
		return ua[:userAgentLimit]
	}
	return ua
}

// noResponseKind 判断没拿到上游响应的失败属于哪一类。最后一次尝试是连不上（含流式第一帧之前断开、空闲超时）时
// 按那次尝试的分类记。
func noResponseKind(err *Error, tr *trace) string {
	switch {
	case errors.Is(err.Err, ErrModelNotFound):
		return KindModelNotFound
	case errors.Is(err.Err, context.Canceled), errors.Is(err.Err, context.DeadlineExceeded):
		return KindCanceled
	case errors.Is(err.Err, antigravity.ErrResponseTooLarge):
		return KindResponseTooLarge
	}
	if n := len(tr.attempts); n > 0 && connectFailure(tr.attempts[n-1].Result) {
		return tr.attempts[n-1].Result
	}
	return KindNoAccount
}

// connectFailure 报告尝试结果是不是没拿到可用的上游响应：连不上上游、连不上代理、流式第一帧之前空闲超时。
func connectFailure(result string) bool {
	return result == KindTransport || result == KindProxy || result == KindStreamIdle
}

// recordNoResponse 为没拿到任何上游响应就失败的请求记一条用量：状态码是返回给客户端的（客户端取消记 499），
// 账号是最后尝试的那个（没尝试过为 0），详情里带尝试列表和失败原因。
func (r *Relay) recordNoResponse(ctx context.Context, req Request, tr *trace, err *Error) {
	kind := noResponseKind(err, tr)
	status := err.Status
	// 失败说明记真正的原因：取消、连不上时回给客户端的是通用说明（upstream is temporarily unavailable），
	// 记进用量没有信息量，还会让人以为是上游出了问题。
	msg := err.Message
	var accountID int64
	if n := len(tr.attempts); n > 0 {
		last := tr.attempts[n-1]
		accountID = last.AccountID
		if connectFailure(kind) && last.Error != "" {
			msg = last.Error
		}
	}
	if kind == KindCanceled {
		status, msg = StatusClientClosed, "客户端在拿到结果前断开"
	}
	var timings *store.UsageTimings
	if wait := tr.cooldownWaitMs(); wait != nil {
		timings = &store.UsageTimings{CooldownWaitMs: wait}
	}
	r.enqueueUsage(r.rejectedRecord(ctx, req, tr.start, accountID, status, store.UsageDetail{
		Timings:    timings,
		Attempts:   tr.attempts,
		Error:      &store.UsageError{Kind: kind, Message: msg},
		FakeStream: req.FakeStream,
	}))
}

// RecordRejected 记一条在网关这一层就被拒绝、没有经过上游的请求（API Key 的额度用完、不允许该模型）。
// start 是收到请求的时间，status 是返回给客户端的状态码。
func (r *Relay) RecordRejected(ctx context.Context, req Request, start time.Time, status int, kind, message string) {
	r.enqueueUsage(r.rejectedRecord(ctx, req, start, 0, status, store.UsageDetail{
		Error:      &store.UsageError{Kind: kind, Message: message},
		FakeStream: req.FakeStream,
	}))
}

func (r *Relay) rejectedRecord(ctx context.Context, req Request, start time.Time, accountID int64, status int, detail store.UsageDetail) *store.UsageRecord {
	now := time.Now()
	raw, err := json.Marshal(detail)
	if err != nil {
		slog.ErrorContext(ctx, "用量详情序列化失败", "err", err)
	}
	return &store.UsageRecord{
		RequestID:    logx.RequestID(ctx),
		APIKeyID:     req.APIKeyID,
		AccountID:    accountID,
		RequestModel: req.requestModel(),
		Protocol:     req.Protocol,
		StatusCode:   status,
		Stream:       req.Stream,
		LatencyMs:    now.Sub(start).Milliseconds(),
		CreatedAt:    now.UnixMilli(),
		ClientIP:     req.ClientIP,
		UserAgent:    clientUA(req.UserAgent),
		Detail:       raw,
	}
}
