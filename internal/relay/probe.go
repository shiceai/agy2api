package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/pool"
)

// 账号检测：用指定账号发一条测试消息，看它现在能不能正常生成。
const (
	probePrompt   = "Hi"
	probeTimeout  = 60 * time.Second
	probeReplyLen = 200
	// ProbeKeyID 是账号检测记进用量时的 API Key ID：真实的 Key 从 1 开始，0 表示管理端的账号检测。
	ProbeKeyID = 0
)

// ErrProbeModel 表示检测的模型不在账号的模型目录里。
var ErrProbeModel = errors.New("relay: model not in account catalog")

// ProbeResult 是一次账号检测的结果。Kind 是失败分类（同用量记录的结果分类，取令牌失败为 transport）。
type ProbeResult struct {
	OK        bool   `json:"ok"`
	Model     string `json:"model"`
	Status    int    `json:"status,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Message   string `json:"message,omitempty"`
	Reply     string `json:"reply,omitempty"`
	LatencyMs int64  `json:"latency_ms"`
	// CooldownMs、Disabled 是这次检测失败引起的冷却和停用（停用原因），和正常请求一样生效。
	CooldownMs int64  `json:"cooldown_ms,omitempty"`
	Disabled   string `json:"disabled,omitempty"`
}

// Probe 用指定账号发一条测试消息（非流式），req.Model 是账号目录里的模型 ID，req.ClientIP、UserAgent 是管理端
// 调用方。和正常请求走同一条调用路径（callAccount），请求形态完全一样，结果也一样生效：限流、额度耗尽冷却，
// 条款封禁、refresh token 失效停用；401 刷新令牌后重试一次。拿到上游响应的检测记进用量（API Key 为 ProbeKeyID），
// 和正常请求一样；取令牌、连接失败的不记（正常请求也不记）。不选号、不占并发名额、不看冷却；停用的账号也能检测
// （确认是否恢复），通过后不自动启用。每次检测都消耗真实额度，只由管理端手动触发。
// 账号不存在返回 store.ErrNotFound，模型不在目录里返回 ErrProbeModel。
func (r *Relay) Probe(ctx context.Context, accountID int64, req Request) (ProbeResult, error) {
	a, err := r.store.GetAccount(ctx, r.enc, accountID)
	if err != nil {
		return ProbeResult{}, err
	}
	account := pool.NewAccount(a)
	if _, ok := account.Model(req.Model); !ok {
		return ProbeResult{}, ErrProbeModel
	}
	req.Body = map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": probePrompt}}}},
	}
	req.Stream, req.APIKeyID = false, ProbeKeyID

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	start := time.Now()
	res := r.probe(ctx, pool.DetachedLease(account, req.Model), req, &trace{start: start})
	res.Model = req.Model
	res.LatencyMs = time.Since(start).Milliseconds()
	slog.InfoContext(ctx, "账号检测", "account_id", accountID, "model", req.Model, "ok", res.OK, "status", res.Status,
		"kind", res.Kind, "latency_ms", res.LatencyMs, "disabled", res.Disabled)
	return res, nil
}

// probe 发出检测请求，处理结果并记用量；耗时和模型由 Probe 填。
func (r *Relay) probe(ctx context.Context, lease *pool.Lease, req Request, tr *trace) ProbeResult {
	var res ProbeResult
	started := time.Now()
	cfg := r.config()
	up, payload, tokenDur, err := r.callAccount(ctx, cfg, lease, req)
	if err == nil && up.Status == http.StatusUnauthorized {
		up.Body.Close()
		r.tokens.Invalidate(lease.Account.ID)
		var retryToken time.Duration
		up, payload, retryToken, err = r.callAccount(ctx, cfg, lease, req)
		tokenDur += retryToken
	}
	if err != nil {
		res.Kind, res.Message = r.transportFailure(ctx, lease.Account.ID, err), err.Error()
		if antigravity.IsInvalidGrant(err) {
			// accessToken 已经停用了账号
			res.Kind, res.Disabled = string(antigravity.KindUnauthorized), ReasonInvalidGrant
		}
		return res
	}
	c := &call{try: 1, started: started, lease: lease, up: up, tokenDur: tokenDur, payload: payload}
	res.Status = up.Status
	if up.Status >= 200 && up.Status < 300 {
		r.pool.NoteSuccess(lease.Account.ID, 0)
		// 和正常请求一样经 Response.Close 记用量（成功的尝试耗时算到读完）。
		resp := &Response{Status: up.Status, Body: up.Body, relay: r, cfg: cfg, req: req, requestID: logx.RequestID(ctx), trace: tr, call: c}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		resp.Close()
		res.OK = true
		res.Reply = replyText(raw)
		return res
	}
	raw, _ := io.ReadAll(io.LimitReader(up.Body, errorBodyLimit))
	up.Body.Close()
	now := time.Now()
	p := cfg.policy
	ue := antigravity.ClassifyError(up.Status, up.Header, raw, now)
	d := decide(ue, p, now)
	cooldown, _ := r.applyDecision(ctx, lease.Account, lease.Model, ue, d, p)
	tr.add(c.try, lease, started, up.Status, string(ue.Kind), ue.Message, cooldown)
	r.recordFailure(ctx, req, tr, c, ue)
	res.CooldownMs = cooldown.Milliseconds()
	res.Kind, res.Message, res.Disabled = string(ue.Kind), ue.Message, d.disable
	return res
}

// replyText 取 Gemini 响应第一个候选里的正文（跳过思考），截到 probeReplyLen 个字符。
func replyText(raw []byte) string {
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(raw, &resp) != nil || len(resp.Candidates) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range resp.Candidates[0].Content.Parts {
		if !p.Thought {
			b.WriteString(p.Text)
		}
	}
	text := []rune(strings.TrimSpace(b.String()))
	if len(text) > probeReplyLen {
		return string(text[:probeReplyLen]) + "…"
	}
	return string(text)
}
