// Package relay 是与入站协议无关的执行层：选号、取令牌、构造上游请求、
// 按上游错误换号重试、冷却或停用账号、写用量记录。
//
// 入站协议（目前只有 Gemini v1beta）负责解析和回写 HTTP，把请求整理成 Gemini 原生形态后交给它；
// 失败时拿到类型化的 *Error，据此决定返回码和 Retry-After。
package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/pool"
	"github.com/shiceai/agy2api/internal/pricing"
	"github.com/shiceai/agy2api/internal/store"
)

const (
	countTokensTimeout = 30 * time.Second
	// 没有账号能解析请求的模型时刷新模型目录，最多每 catalogRefreshInterval 一次。发起请求的客户端在等它，
	// 所以各账号并发拉取（最多 catalogRefreshParallel 个同时），整体最多 catalogRefreshTimeout。
	catalogRefreshInterval = 5 * time.Minute
	catalogRefreshTimeout  = 20 * time.Second
	catalogRefreshParallel = 4
	errorBodyLimit         = 1 << 20
)

// ErrNoAccount 表示当前没有可用账号。
var ErrNoAccount = pool.ErrNoAccount

// ErrModelNotFound 表示没有账号的目录能解析出请求的模型。
var ErrModelNotFound = pool.ErrModelNotFound

// Relay 持有号池、令牌缓存和上游客户端。
type Relay struct {
	client *antigravity.Client
	tokens *antigravity.TokenManager
	pool   *pool.Pool
	store  *store.Store
	enc    store.Encryptor
	usage  *usageWriter
	quota  *quotaRefresher // 后台额度刷新，StartQuotaRefresh 后非空
	// onUsage 在每条用量记录排队落库前调用（API Key 的 Token 上限计数用），SetUsageHook 在开始服务前设置。
	onUsage func(*store.UsageRecord)
	// prices 按模型价格算用量的费用，SetPricing 在开始服务前设置；为 nil 时不算。
	prices *pricing.Service

	affinity *affinityStore // 会话粘性：对话上次用的账号

	// cfg 是运行时配置快照（见 config.go），cfgMu 串行化修改。
	cfgMu sync.Mutex
	cfg   atomic.Pointer[runtimeConfig]

	// catalogMu 串行化目录刷新，lastCatalogRefresh 用于限频。
	catalogMu          sync.Mutex
	lastCatalogRefresh time.Time
}

// SetUsageHook 设置每条用量记录排队落库前的回调，只能在开始服务前调用一次。回调在请求路径上同步执行，要快。
func (r *Relay) SetUsageHook(fn func(*store.UsageRecord)) {
	r.onUsage = fn
}

// SetPricing 设置算用量费用的价格服务，只能在开始服务前调用一次。
func (r *Relay) SetPricing(p *pricing.Service) {
	r.prices = p
}

// New 构造执行层并启动用量写协程，设置为 DefaultPolicy。号池为空，使用前需调用 Reload 和 LoadCooldowns；
// 用完调用 Close。
func New(client *antigravity.Client, st *store.Store, enc store.Encryptor) *Relay {
	r := &Relay{client: client, pool: pool.New(st, enc), store: st, enc: enc, usage: newUsageWriter(st),
		affinity: newAffinityStore(affinityLimit)}
	// refresh token 轮换时回写数据库并刷新号池。
	r.tokens = antigravity.NewTokenManager(client, r.persistRotatedToken)
	r.cfg.Store(&runtimeConfig{})
	r.SetPolicy(DefaultPolicy())
	return r
}

// Reload 从库重新加载号池。启动时和账号变更落库后调用。
func (r *Relay) Reload(ctx context.Context) error {
	return r.pool.Reload(ctx)
}

// LoadCooldowns 从库读回未过期的冷却。启动时调用一次。
func (r *Relay) LoadCooldowns(ctx context.Context) error {
	return r.pool.LoadCooldowns(ctx)
}

// Cooldowns 返回生效中的冷却，按账号分组。
func (r *Relay) Cooldowns() map[int64][]pool.CooldownInfo {
	return r.pool.Cooldowns()
}

// Close 停止后台额度刷新和接收用量记录，等排队的记录写完；ctx 到期则不再等待。应在 HTTP 服务停止后调用。
func (r *Relay) Close(ctx context.Context) error {
	r.stopQuotaRefresh(ctx)
	return r.usage.Close(ctx)
}

// Paused 返回因代理连不上暂停调度的账号和截止时间。
func (r *Relay) Paused() map[int64]time.Time {
	return r.pool.Paused()
}

// InFlight 返回各账号当前进行中的生成请求数（账号 ID → 并发占用，为 0 的不在表里）。
func (r *Relay) InFlight() map[int64]int {
	return r.pool.InFlightAll()
}

// InvalidateToken 清除账号缓存的 access token，下次请求时重新刷新。
func (r *Relay) InvalidateToken(accountID int64) {
	r.tokens.Invalidate(accountID)
}

// Models 返回启用账号模型目录的并集。
func (r *Relay) Models() []antigravity.ModelInfo {
	return r.pool.Models()
}

// KnownModel 报告号池目录里有没有这个模型（精确 ID 或家族名），只查内存、不刷新目录。模型别名解析用它
// 判断客户端的模型名要不要映射。
func (r *Relay) KnownModel(requested string) bool {
	_, ok := r.pool.Lookup(requested, r.config().configuredTier(requested))
	return ok
}

// LookupModel 解析请求的模型名（支持家族名，按该家族的配置档位选档），返回解析后的模型信息。
// 解析不出时先刷新模型目录（限频）再试一次。
func (r *Relay) LookupModel(ctx context.Context, requested string) (antigravity.ModelInfo, bool) {
	tier := r.config().configuredTier(requested)
	if m, ok := r.pool.Lookup(requested, tier); ok {
		return m, true
	}
	if r.refreshCatalogs(ctx, requested, "没有账号支持该模型") {
		return r.pool.Lookup(requested, tier)
	}
	return antigravity.ModelInfo{}, false
}

// acquire 选号并占用并发名额，并发都满时最多等 wait。没有账号能解析该模型，或选中的模型在目录里缺枚举名
// （升级前存下的旧目录）时，先刷新模型目录（限频）再选一次。refreshed 记录本次请求是否已经尝试过刷新，
// 避免换号重试时重复刷新。tried 是本次请求已经试过的账号，prefer 是会话粘性优先的账号（0 表示没有）。
func (r *Relay) acquire(ctx context.Context, requested, tier string, tried map[int64]bool, prefer int64, refreshed *bool, wait time.Duration) (*pool.Lease, error) {
	lease, err := r.pool.Acquire(ctx, requested, tier, tried, prefer, wait)
	reason := ""
	switch {
	case errors.Is(err, ErrModelNotFound):
		reason = "没有账号支持该模型"
	case err == nil && lease.Account.MissingEnum(lease.Model):
		reason = "目录缺模型枚举名"
	}
	if reason != "" && !*refreshed {
		*refreshed = true
		if r.refreshCatalogs(ctx, requested, reason) {
			if lease != nil {
				lease.Release()
			}
			lease, err = r.pool.Acquire(ctx, requested, tier, tried, prefer, wait)
		}
	}
	return lease, err
}

// refreshCatalogs 重新拉取各账号的模型目录，落库并刷新号池，返回目录是否可能有变化。
// 最多每 catalogRefreshInterval 刷新一次；并发请求共用一次刷新：等锁期间别人刚刷新完的，直接用新目录。
// 不随请求取消，避免刷新到一半被打断。
func (r *Relay) refreshCatalogs(ctx context.Context, requested, reason string) bool {
	waitStart := time.Now()
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	if r.lastCatalogRefresh.After(waitStart) {
		return true
	}
	if time.Since(r.lastCatalogRefresh) < catalogRefreshInterval {
		return false
	}
	defer func() { r.lastCatalogRefresh = time.Now() }()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), catalogRefreshTimeout)
	defer cancel()
	accounts := r.pool.Accounts()
	slog.InfoContext(ctx, "刷新模型目录", "reason", reason, "model", requested, "accounts", len(accounts))
	var updated atomic.Int32
	var wg sync.WaitGroup
	slots := make(chan struct{}, catalogRefreshParallel)
	for _, a := range accounts {
		if a.ProjectID == "" {
			continue
		}
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			if r.refreshAccountCatalog(ctx, a) {
				updated.Add(1)
			}
		})
	}
	wg.Wait()
	if updated.Load() == 0 {
		return false
	}
	if err := r.pool.Reload(ctx); err != nil {
		slog.ErrorContext(ctx, "刷新号池失败", "err", err)
		return false
	}
	r.pool.ReconcileCooldowns(ctx)
	return true
}

// refreshAccountCatalog 拉取一个账号的模型目录并落库，返回是否写入了新目录。失败只记日志。
func (r *Relay) refreshAccountCatalog(ctx context.Context, a *pool.Account) bool {
	token, err := r.accessToken(ctx, a)
	if err != nil {
		slog.WarnContext(ctx, "刷新模型目录失败：取令牌出错", "account_id", a.ID, "err", err)
		return false
	}
	models, err := r.client.FetchAvailableModels(ctx, controlCredential(a, token))
	if err != nil {
		slog.WarnContext(ctx, "刷新模型目录失败", "account_id", a.ID, "err", err)
		return false
	}
	if len(models) == 0 {
		return false // 空目录会让账号变成"放行任何模型"，不覆盖
	}
	if err := r.store.UpdateAccountModels(ctx, a.ID, antigravity.MarshalModels(models)); err != nil {
		slog.ErrorContext(ctx, "保存模型目录失败", "account_id", a.ID, "err", err)
		return false
	}
	return true
}

// Families 返回启用账号目录里的分档家族及其可用档位。
func (r *Relay) Families() map[string][]string {
	models := r.pool.Models()
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return antigravity.TierFamilies(ids)
}

// inlineRemoteImages 按配置把请求里的 http(s) 图片 URL 下载后内联（原地改 req.Body）。
// 失败记一条用量并返回 400；未开启时不动。
func (r *Relay) inlineRemoteImages(ctx context.Context, cfg *runtimeConfig, req Request, start time.Time) *Error {
	if !cfg.remoteImages {
		return nil
	}
	n, err := antigravity.InlineRemoteImages(ctx, req.Body)
	if err != nil {
		var rie *antigravity.RemoteImageError
		msg := "could not inline a remote image"
		if errors.As(err, &rie) {
			msg = rie.Error()
		}
		slog.InfoContext(ctx, "远程图片内联失败", "model", req.Model, "err", err)
		r.RecordRejected(ctx, req, start, http.StatusBadRequest, KindRemoteImage, msg)
		return &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: msg, Err: err}
	}
	if n > 0 {
		slog.DebugContext(ctx, "远程图片内联", "count", n, "model", req.Model)
	}
	return nil
}

// disguiseSystemPrompt 按配置把官方基线 system 提示词注入请求体（原地改 req.Body）。未配置时不动。
func (r *Relay) disguiseSystemPrompt(ctx context.Context, cfg *runtimeConfig, req Request) {
	c := cfg.systemPrompt
	if c == nil {
		return
	}
	if antigravity.ApplySystemPrompt(req.Body, c.mode, c.template) {
		slog.DebugContext(ctx, "系统提示词伪装：已注入", "mode", string(c.mode), "model", req.Model)
	}
}

// filterClientNames 按配置对请求的 system 提示词做客户端名称过滤：替换模式改一次 req.Body（后续换号沿用），
// 拦截模式命中时记一条用量并返回 400，不选号、不发上游。未配置或无命中时返回 nil。
func (r *Relay) filterClientNames(ctx context.Context, cfg *runtimeConfig, req Request, start time.Time) *Error {
	f := cfg.clientFilter
	if f.Empty() {
		return nil
	}
	if f.Mode() == antigravity.FilterBlock {
		if f.Scan(req.Body) > 0 {
			const msg = "system prompt matches a blocked client-name rule"
			slog.InfoContext(ctx, "客户端名称过滤：拦截请求", "model", req.Model)
			r.RecordRejected(ctx, req, start, http.StatusBadRequest, KindClientFiltered, msg)
			return &Error{Status: http.StatusBadRequest, Code: "INVALID_ARGUMENT", Message: msg}
		}
		return nil
	}
	if n := f.Apply(req.Body); n > 0 {
		slog.InfoContext(ctx, "客户端名称过滤：已替换 system 提示词", "hits", n, "model", req.Model)
	}
	return nil
}

// Credential 从库里的账号构造上游凭据。
func Credential(a *store.Account) antigravity.Credential {
	return antigravity.Credential{
		RefreshToken:  a.RefreshToken,
		ClientID:      a.ClientID,
		ClientSecret:  a.ClientSecret,
		ProjectID:     a.ProjectID,
		UserProjectID: a.UserProjectID,
		Scope:         a.Scope,
		UserAgent:     a.UserAgent,
		Email:         a.Email,
		Proxy:         a.ProxyURL,
	}
}

// controlCredential 是对账号调控制面（取模型目录、查订阅）用的凭据：从完整的账号信息构造，带着绑定的代理
// （所有请求都经它出网），再填上已取到的 access token。不要手工挑字段拼：漏了 Proxy 就会从本机直连上游。
func controlCredential(a *pool.Account, token string) *antigravity.Credential {
	c := Credential(a.Account)
	c.AccessToken = token
	return &c
}

// Request 是一次生成请求。
type Request struct {
	// Model 是要用的模型名（精确 ID 或家族名，已经过模型别名解析）。
	Model string
	// RequestModel 是客户端请求的原始模型名，记进用量；为空时同 Model。
	RequestModel string
	// Protocol 是入站接口（store.ProtocolGemini、ProtocolAnthropic、ProtocolOpenAI），记进用量；为空按 Gemini。
	Protocol string
	// Body 是 Gemini 原生请求体（contents、tools、generationConfig 等）。
	Body     map[string]any
	Stream   bool
	APIKeyID int64
	// ClientIP、UserAgent 是调用方的 IP 和 User-Agent，记进用量。
	ClientIP  string
	UserAgent string
	// FakeStream 表示客户端请求的是假流式（Stream 为 false，结果由入站协议按流式返回），只记进用量详情。
	FakeStream bool
}

// requestModel 返回记进用量的请求模型：客户端原始的模型名。
func (req Request) requestModel() string {
	if req.RequestModel != "" {
		return req.RequestModel
	}
	return req.Model
}

// Response 是一次成功（2xx）的生成调用，Body 是标准 Gemini 形态（非流式为完整 JSON，流式为逐条解包的 SSE）。
// 调用方读完 Body 后必须调用 Close：归还账号的并发名额，写入用量记录。
type Response struct {
	Status int
	Body   io.Reader

	relay     *Relay
	cfg       *runtimeConfig // 这次请求用的配置快照
	req       Request
	requestID string
	trace     *trace
	call      *call
	closed    bool
}

// Close 关闭上游响应、归还并发名额并写入用量记录，重复调用无副作用。
func (resp *Response) Close() error {
	if resp.closed {
		return nil
	}
	resp.closed = true
	err := resp.call.up.Body.Close()
	resp.call.lease.Release()
	resp.relay.recordUsage(resp)
	return err
}

// Generate 选号并调用上游生成，成功时返回 2xx 的 Response；失败时返回 *Error。
//
// 401 刷新令牌后同号重试一次；其余非 2xx 按 decide 分类：限流、5xx、仍 401 冷却或直接换号重试，
// 最多 Policy.MaxAttempts 个账号；403 和请求本身的错误（4xx）不换号，改写后返回。
// 取令牌失败、连接失败也换号；refresh token 失效（invalid_grant）的账号会被停用。
// 流式先读到第一帧才返回（见 firstFrame），第一帧之前的错误也按上面的规则换号。
// 请求的模型按各账号目录解析（家族名按档位选），没有账号能解析时先刷新目录，仍不行返回 404。
func (r *Relay) Generate(ctx context.Context, req Request) (*Response, error) {
	start := time.Now()
	cfg := r.config() // 整个请求（含换号重试）用同一份配置
	if err := r.filterClientNames(ctx, cfg, req, start); err != nil {
		return nil, err
	}
	// 先清洗客户端 system 里的客户端名，再注入/伪装官方 system。
	r.disguiseSystemPrompt(ctx, cfg, req)
	if err := r.inlineRemoteImages(ctx, cfg, req, start); err != nil {
		return nil, err
	}
	p := cfg.policy
	tier := cfg.tier(req.Model, req.Body)
	refreshed := false
	tried := map[int64]bool{}
	tr := &trace{start: start, fakeStream: req.FakeStream}
	// 会话粘性：同一对话优先用上次成功服务它的账号。
	stickyKey := ""
	var prefer int64
	if p.Sticky {
		if stickyKey = affinityKey(req); stickyKey != "" {
			prefer = r.affinity.get(stickyKey, start, p.StickyTTL)
		}
	}
	var last *failure
	var minCooldown time.Duration
	// readyAt 是这一轮里试过的账号冷却最早结束的时间（试过的账号不参与选号，号池给的最早恢复时间不含它们），
	// waitLeft 是还能等冷却的时间，见 waitCooldown。
	var readyAt time.Time
	waitLeft := p.CooldownWait
	// lastUpstream 是最近一次换号重试前的上游失败，最终失败时据此补记一条用量。
	var lastUpstream *call
	var lastErr antigravity.UpstreamError
	fail := func(err *Error) (*Response, error) {
		if lastUpstream != nil {
			r.recordFailure(ctx, req, tr, lastUpstream, lastErr)
		} else {
			r.recordNoResponse(ctx, req, tr, err)
		}
		return nil, err
	}
	// canceled 结束客户端已经取消的请求：不再换号，按取消记一条用量（499），之前的上游失败只留在尝试列表里。
	canceled := func() (*Response, error) {
		err := errUnavailable(ctx.Err())
		r.recordNoResponse(ctx, req, tr, err)
		return nil, err
	}
	for try := 1; try <= p.MaxAttempts; try++ {
		attemptStart := time.Now()
		lease, err := r.acquire(ctx, req.Model, tier, tried, prefer, &refreshed, p.SlotWait)
		// 选不到账号、而最早恢复的账号很快就能用：等到那时再选一轮，试过的账号也重新参与。
		for err != nil && waitCooldown(ctx, err, readyAt, &waitLeft, tr, req.Model) {
			clear(tried)
			readyAt = time.Time{}
			attemptStart = time.Now()
			lease, err = r.acquire(ctx, req.Model, tier, tried, prefer, &refreshed, p.SlotWait)
		}
		acquireDur := time.Since(attemptStart)
		if err != nil {
			if errors.Is(err, ErrModelNotFound) {
				slog.WarnContext(ctx, "没有账号支持该模型", "model", req.Model, "tier", tier)
				return fail(errModelNotFound(req.Model))
			}
			if ctx.Err() != nil {
				return canceled()
			}
			slog.WarnContext(ctx, "没有可用账号", "model", req.Model, "try", try, "err", err)
			return fail(exhaustedError(err, last, minCooldown))
		}
		tried[lease.Account.ID] = true
		if stickyKey != "" {
			tr.affinity = r.affinityDetail(prefer, lease)
		}
		log := slog.With("account_id", lease.Account.ID, "model", req.Model, "resolved_model", lease.Model, "stream", req.Stream, "try", try)
		log.DebugContext(ctx, "选号", "affinity", lease.Affinity)

		up, tokenDur, err := r.callAccount(ctx, cfg, lease, req)
		if err == nil && up.Status == http.StatusUnauthorized {
			// 刷新令牌后在同账号重试一次。
			up.Body.Close()
			log.InfoContext(ctx, "上游返回 401，刷新令牌后重试")
			r.tokens.Invalidate(lease.Account.ID)
			var retryToken time.Duration
			up, retryToken, err = r.callAccount(ctx, cfg, lease, req)
			tokenDur += retryToken
		}
		if err != nil {
			lease.Release()
			if ctx.Err() != nil {
				// 客户端断开不是账号的问题：不暂停账号（连代理时被取消也报 proxyconnect），不换号，
				// 也不替别的账号刷新令牌。
				log.InfoContext(ctx, "客户端已取消，停止尝试", "err", err)
				tr.add(try, lease, attemptStart, 0, KindCanceled, err.Error(), 0)
				return canceled()
			}
			if errors.Is(err, antigravity.ErrResponseTooLarge) {
				// 上游已经生成完（额度花掉了），只是结果太大给不了：账号没问题，不冷却；换号只会再超一次，不换。
				log.WarnContext(ctx, "上游非流式响应超过大小上限", "err", err)
				tr.add(try, lease, attemptStart, http.StatusOK, KindResponseTooLarge, err.Error(), 0)
				tooLarge := errResponseTooLarge(err)
				r.recordNoResponse(ctx, req, tr, tooLarge)
				return nil, tooLarge
			}
			kind := r.transportFailure(ctx, lease.Account.ID, err)
			log.WarnContext(ctx, "上游调用失败，换号重试", "kind", kind, "err", err)
			tr.add(try, lease, attemptStart, 0, kind, err.Error(), 0)
			last = &failure{err: err}
			continue
		}
		c := &call{try: try, started: attemptStart, lease: lease, up: up, acquireDur: acquireDur, tokenDur: tokenDur}
		var ue antigravity.UpstreamError
		if up.Status >= 200 && up.Status < 300 {
			body, err := firstFrame(up, req.Stream)
			if err == nil {
				r.pool.NoteSuccess(lease.Account.ID)
				if stickyKey != "" {
					r.affinity.set(stickyKey, lease.Account.ID, time.Now(), p.StickyTTL)
				}
				log.DebugContext(ctx, "上游响应", "status", up.Status, "proto", up.Proto, "latency_ms", time.Since(start).Milliseconds())
				return &Response{Status: up.Status, Body: body, relay: r, cfg: cfg, req: req, requestID: logx.RequestID(ctx), trace: tr, call: c}, nil
			}
			// 流式在第一帧发给客户端之前就出错：客户端什么都还没收到，可以透明地换号。错误帧按非 2xx 处理（下面），
			// 断开、空闲超时按连接失败处理。
			up.Body.Close()
			var se *antigravity.StreamError
			if !errors.As(err, &se) {
				lease.Release()
				if ctx.Err() != nil {
					log.InfoContext(ctx, "客户端已取消，停止尝试", "err", err)
					tr.add(try, lease, attemptStart, 0, KindCanceled, err.Error(), 0)
					return canceled()
				}
				kind := KindStreamIdle
				if !errors.Is(err, ErrStreamIdle) {
					kind = r.transportFailure(ctx, lease.Account.ID, err)
				}
				log.WarnContext(ctx, "流式第一帧之前读取失败，换号重试", "kind", kind, "err", err)
				tr.add(try, lease, attemptStart, up.Status, kind, err.Error(), 0)
				last = &failure{err: err}
				continue
			}
			ue = se.UpstreamError
		} else {
			body, _ := io.ReadAll(io.LimitReader(up.Body, errorBodyLimit))
			up.Body.Close()
			ue = antigravity.ClassifyError(up.Status, up.Header, body, time.Now())
		}

		now := time.Now()
		d := decide(ue, p, now)
		cooldown := r.applyDecision(ctx, lease.Account, lease.Model, ue, d, p)
		lease.Release()
		log.WarnContext(ctx, "上游返回错误",
			"status", ue.Status, "kind", ue.Kind, "reason", ue.Reason, "action", d.action,
			"cooldown", cooldown, "disable", d.disable, "message", ue.Message)
		tr.add(try, lease, attemptStart, ue.Status, string(ue.Kind), ue.Message, cooldown)
		if d.action == actionReturn {
			r.recordFailure(ctx, req, tr, c, ue)
			return nil, errFromUpstream(ue)
		}
		if cooldown > 0 {
			if minCooldown == 0 || cooldown < minCooldown {
				minCooldown = cooldown
			}
			if until := now.Add(cooldown); readyAt.IsZero() || until.Before(readyAt) {
				readyAt = until
			}
		}
		last = &failure{kind: ue.Kind, err: fmt.Errorf("upstream HTTP %d %s", ue.Status, ue.Kind), cooldown: cooldown}
		lastUpstream, lastErr = c, ue
	}
	return fail(exhaustedError(nil, last, minCooldown))
}

// minCooldownWait 是等冷却的最短时长：冷却刚好到期时也等这么一下，保证等待时间一定会用完，不会原地打转。
const minCooldownWait = 100 * time.Millisecond

// waitCooldown 在选不到账号时决定要不要等冷却：最早恢复的时间（号池给的，和这一轮里试过的账号冷却结束的 readyAt
// 取较早的）在剩余的等待时间 left 内时，等到那时、记进 tr，返回 true，调用方清空试过的账号再选一轮。
// 没有在冷却的账号、等不及或等待期间客户端取消时返回 false。
func waitCooldown(ctx context.Context, err error, readyAt time.Time, left *time.Duration, tr *trace, model string) bool {
	if *left <= 0 || errors.Is(err, ErrModelNotFound) {
		return false
	}
	at := readyAt
	var ue *pool.UnavailableError
	if errors.As(err, &ue) && !ue.RetryAt.IsZero() && (at.IsZero() || ue.RetryAt.Before(at)) {
		at = ue.RetryAt
	}
	if at.IsZero() {
		return false
	}
	d := max(time.Until(at), minCooldownWait)
	if d > *left {
		return false
	}
	slog.InfoContext(ctx, "账号都在冷却，等最早恢复的再试", "model", model, "wait", d)
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	}
	*left -= d
	tr.cooldownWait += d
	return true
}

// firstFrame 让流式响应先读出第一块数据（解包后的第一帧）再交给客户端，返回从这块开始的响应体：
// 上游的响应头和第一帧几乎同时到，基本不增加延迟，但第一帧之前的错误（错误帧、连接断开、空闲超时）
// 还能换号重试，客户端无感。流直接结束（空流）时照常返回。
// 非流式直接返回 Body（错误已经在 antigravity 里识别过）。
func firstFrame(up *antigravity.Response, stream bool) (io.Reader, error) {
	if !stream {
		return up.Body, nil
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := up.Body.Read(buf)
		switch {
		case n > 0:
			return io.MultiReader(bytes.NewReader(buf[:n]), up.Body), nil
		case errors.Is(err, io.EOF):
			return up.Body, nil
		case err != nil:
			return nil, err
		}
	}
}

// applyDecision 执行决定里对账号的处理：冷却或停用。返回实际施加的冷却时长。
func (r *Relay) applyDecision(ctx context.Context, account *pool.Account, model string, ue antigravity.UpstreamError, d decision, p Policy) time.Duration {
	id := account.ID
	if d.disable != "" {
		r.disableAccount(ctx, id, d.disable)
		return 0
	}
	cooldown := d.cooldown
	if d.backoff {
		cooldown = rateLimitBackoff(r.pool.NoteRateLimit(id), p)
	}
	if cooldown > 0 {
		models := []string{model}
		// 额度按窗口算：同窗口的其它模型一起冷却，不再挨个撞 429。额度恢复后由 ReconcileCooldowns 一起解除。
		if ue.Kind == antigravity.KindQuotaExhausted {
			models = append(models, account.SharedQuota(model)...)
		}
		r.pool.Cooldown(ctx, id, time.Now().Add(cooldown), string(ue.Kind), models...)
	}
	return cooldown
}

// disableAccount 自动停用账号并记下原因，刷新号池。不随请求取消。
func (r *Relay) disableAccount(ctx context.Context, id int64, reason string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.store.SetAccountStatus(ctx, id, store.StatusDisabled, reason); err != nil {
		slog.ErrorContext(ctx, "自动停用账号失败", "account_id", id, "reason", reason, "err", err)
		return
	}
	slog.WarnContext(ctx, "已自动停用账号", "account_id", id, "reason", reason)
	if err := r.pool.Reload(ctx); err != nil {
		slog.ErrorContext(ctx, "刷新号池失败", "err", err)
	}
}

// transportFailure 处理没拿到上游响应的失败，返回尝试记录里的分类：代理连不上时暂停这个账号一会儿
// （换号也许能走别的代理），记 KindProxy；其余记 KindTransport。
func (r *Relay) transportFailure(ctx context.Context, accountID int64, err error) string {
	if !antigravity.IsProxyError(err) {
		return KindTransport
	}
	r.pool.Pause(accountID, time.Now().Add(proxyPause))
	slog.WarnContext(ctx, "账号的代理连不上，暂停调度", "account_id", accountID, "pause", proxyPause)
	return KindProxy
}

// callAccount 对选中的账号构造信封并调用上游一次，同时返回取令牌的用时。
func (r *Relay) callAccount(ctx context.Context, cfg *runtimeConfig, lease *pool.Lease, req Request) (*antigravity.Response, time.Duration, error) {
	account := lease.Account
	tokenStart := time.Now()
	token, err := r.accessToken(ctx, account)
	tokenDur := time.Since(tokenStart)
	if err != nil {
		return nil, tokenDur, err
	}
	payload, err := antigravity.BuildEnvelope(buildParams(cfg, account, lease.Model, req.Body, caller(req)))
	if err != nil {
		return nil, tokenDur, err
	}
	// 时间限制见 stream.go。流式响应需要在读取期间保持 ctx，故不能在此 defer cancel；交由 body 关闭时回收。
	callCtx, cancel, headerArrived, headerTimedOut := startCall(ctx, req.Stream)
	resp, err := r.client.Generate(callCtx, account.ProxyURL, token, r.client.UserAgentFor(account.UserAgent, account.Email), payload, req.Stream, cfg.hideThoughts)
	headerArrived()
	if err != nil {
		cancel()
		if headerTimedOut() {
			err = errHeaderTimeout
		}
		return nil, tokenDur, err
	}
	if req.Stream && resp.Status >= 200 && resp.Status < 300 {
		resp.Body = watchIdle(resp, cfg.policy.StreamIdleTimeout, cancel)
	} else {
		resp.Body = withCancel(resp.Body, cancel)
	}
	return resp, tokenDur, nil
}

// CountTokens 选一个账号调用上游 countTokens，返回标准 Gemini JSON。不等并发名额。
// 没有账号支持该模型时返回 ErrModelNotFound；其它 error 调用方可回退到本地估算。
func (r *Relay) CountTokens(ctx context.Context, requested string, body map[string]any) ([]byte, error) {
	cfg := r.config()
	refreshed := false
	lease, err := r.acquire(ctx, requested, cfg.tier(requested, body), nil, 0, &refreshed, 0)
	if err != nil {
		return nil, err
	}
	defer lease.Release()
	token, err := r.accessToken(ctx, lease.Account)
	if err != nil {
		return nil, err
	}
	payload, err := antigravity.BuildCountTokensBody(buildParams(cfg, lease.Account, lease.Model, body, ""))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, countTokensTimeout)
	defer cancel()
	return r.client.CountTokens(ctx, lease.Account.ProxyURL, token, r.client.UserAgentFor(lease.Account.UserAgent, lease.Account.Email), payload)
}

// caller 标识调用方（API Key），和首条用户消息一起认对话：派生会话 ID、会话粘性。
func caller(req Request) string {
	return fmt.Sprintf("key:%d", req.APIKeyID)
}

// buildParams 组装信封参数。caller 标识调用方（API Key），用于派生对话的会话 ID。
func buildParams(cfg *runtimeConfig, account *pool.Account, model string, body map[string]any, caller string) antigravity.BuildParams {
	p := antigravity.BuildParams{
		ProjectID:     account.ProjectID,
		WireModel:     model,
		AccountID:     account.ID,
		GeminiRequest: body,
		Caller:        caller,
		Email:         account.Email,
		HideThoughts:  cfg.hideThoughts,
	}
	if m, ok := account.Model(model); ok {
		p.Model = &m
	}
	return p
}

// accessToken 取账号当前 access token（必要时刷新）。refresh token 已失效（invalid_grant）时停用账号。
func (r *Relay) accessToken(ctx context.Context, account *pool.Account) (string, error) {
	token, err := r.tokens.AccessToken(ctx, account.ID, Credential(account.Account))
	if err != nil && antigravity.IsInvalidGrant(err) {
		r.disableAccount(ctx, account.ID, ReasonInvalidGrant)
	}
	return token, err
}

// persistRotatedToken 在刷新轮换 refresh token 后回写数据库，并刷新号池快照，
// 保证下次刷新用的是新 refresh token。
func (r *Relay) persistRotatedToken(accountID int64, cred antigravity.Credential) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Refresh 成功后 cred 总带着实际使用的 client 凭据。
	if err := r.store.UpdateAccountCredentials(ctx, r.enc, accountID, cred.RefreshToken, cred.ClientID, cred.ClientSecret); err != nil {
		slog.Error("回写轮换的 refresh token 失败", "account_id", accountID, "err", err)
		return
	}
	slog.Info("已回写轮换的 refresh token", "account_id", accountID)
	if err := r.pool.Reload(ctx); err != nil {
		slog.Error("刷新号池失败", "err", err)
	}
}

// userAgentLimit 是记进用量的 User-Agent 最大长度。
const userAgentLimit = 512

// usageRecord 生成一条用量记录的公共部分：c 是最终那次尝试。
func usageRecord(requestID string, req Request, tr *trace, c *call) *store.UsageRecord {
	now := time.Now()
	return &store.UsageRecord{
		RequestID:     requestID,
		APIKeyID:      req.APIKeyID,
		AccountID:     c.lease.Account.ID,
		RequestModel:  req.requestModel(),
		ResolvedModel: c.lease.Model,
		Protocol:      req.Protocol,
		Reasoning:     antigravity.ReasoningLabel(c.lease.Model, req.Body),
		StatusCode:    c.up.Status,
		Stream:        req.Stream,
		LatencyMs:     now.Sub(tr.start).Milliseconds(),
		CreatedAt:     now.UnixMilli(),
		ClientIP:      req.ClientIP,
		UserAgent:     clientUA(req.UserAgent),
	}
}

// recordUsage 在成功响应关闭时生成用量记录，交给写协程异步落库，避开请求热路径。
// 成功的那次尝试在这里才记进尝试列表：耗时要算到流读完。
//
// 流式中途出错时客户端收到的是 200 加一个错误事件，用量记成失败，Token 记已收到的部分：空闲断开记 504；
// 上游推来错误帧记它的状态码，并且和非 2xx 一样处理账号（冷却、停用）。
func (r *Relay) recordUsage(resp *Response) {
	c, up := resp.call, resp.call.up
	rec := usageRecord(resp.requestID, resp.req, resp.trace, c)
	var uerr *store.UsageError
	switch se := up.StreamError(); {
	case streamIdled(up.Body):
		uerr = &store.UsageError{Kind: KindStreamIdle,
			Message: fmt.Sprintf("上游超过 %s 没有数据，已断开", resp.cfg.policy.StreamIdleTimeout)}
		rec.StatusCode = http.StatusGatewayTimeout
		resp.trace.add(c.try, c.lease, c.started, up.Status, KindStreamIdle, uerr.Message, 0)
	case se != nil:
		cooldown := r.midStreamError(resp, se.UpstreamError)
		uerr = &store.UsageError{Kind: string(se.Kind), Reason: se.Reason, Message: se.Message}
		rec.StatusCode = se.Status
		resp.trace.add(c.try, c.lease, c.started, se.Status, string(se.Kind), se.Message, cooldown)
	default:
		resp.trace.add(c.try, c.lease, c.started, up.Status, "success", "", 0)
	}
	usage := up.Usage
	if usage == nil {
		usage = up.StreamUsage()
	}
	if usage != nil {
		rec.UpstreamModel = usage.UpstreamModel
		rec.PromptTokens = usage.PromptTokens
		rec.OutputTokens = usage.OutputTokens
		rec.TotalTokens = usage.TotalTokens
		rec.CachedTokens = &usage.CachedTokens
		rec.ThoughtsTokens = &usage.ThoughtsTokens
	}
	cost := r.cost(rec)
	if cost != nil {
		rec.CostNanos = &cost.Nanos
	}
	rec.FirstTokenMs = offset(resp.trace.start, up.FirstTokenAt())
	rec.Detail = resp.trace.detail(c, uerr, cost)
	r.enqueueUsage(rec)
}

// midStreamError 处理上游在流式中途推来的错误帧：和非 2xx 一样冷却或停用账号。这次请求已经开始输出，不再换号。
// 返回施加的冷却时长。在响应关闭时调用，请求的 ctx 可能已经取消，日志另带请求 ID。
func (r *Relay) midStreamError(resp *Response, ue antigravity.UpstreamError) time.Duration {
	ctx := logx.WithRequestID(context.Background(), resp.requestID)
	lease, p := resp.call.lease, resp.cfg.policy
	d := decide(ue, p, time.Now())
	cooldown := r.applyDecision(ctx, lease.Account, lease.Model, ue, d, p)
	slog.WarnContext(ctx, "上游在流式中途返回错误",
		"account_id", lease.Account.ID, "model", resp.req.Model, "resolved_model", lease.Model,
		"status", ue.Status, "kind", ue.Kind, "reason", ue.Reason, "cooldown", cooldown, "disable", d.disable, "message", ue.Message)
	return cooldown
}

// recordFailure 为最终失败的请求记一条用量：c 是决定了结果的那次尝试（没有 token），状态码记分类时用的上游状态码
// （2xx 里带错误的按错误的状态码），详情里带上全部尝试和失败原因。换号重试中途的失败只进尝试列表，不单独记。
func (r *Relay) recordFailure(ctx context.Context, req Request, tr *trace, c *call, ue antigravity.UpstreamError) {
	rec := usageRecord(logx.RequestID(ctx), req, tr, c)
	rec.StatusCode = ue.Status
	rec.Detail = tr.detail(c, &store.UsageError{Kind: string(ue.Kind), Reason: ue.Reason, Message: ue.Message}, nil)
	r.enqueueUsage(rec)
}

// cost 按记录时的价格算费用（实际发给上游的模型）；没有价格服务、没有 token 或没有价格时返回 nil，之后有了价格再补算。
func (r *Relay) cost(rec *store.UsageRecord) *store.UsageCost {
	if r.prices == nil {
		return nil
	}
	u := pricing.Usage{Prompt: rec.PromptTokens, Output: rec.OutputTokens}
	if rec.CachedTokens != nil {
		u.Cached = *rec.CachedTokens
	}
	if rec.ThoughtsTokens != nil {
		u.Thoughts = *rec.ThoughtsTokens
	}
	return r.prices.Cost(rec.ResolvedModel, time.UnixMilli(rec.CreatedAt), u)
}

// enqueueUsage 调用用量回调后把记录交给写协程。
func (r *Relay) enqueueUsage(rec *store.UsageRecord) {
	if r.onUsage != nil {
		r.onUsage(rec)
	}
	r.usage.Enqueue(rec)
}
