package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/pool"
	"github.com/shiceai/agy2api/internal/relay"
	"github.com/shiceai/agy2api/internal/store"
)

// --- 账号 ---

type accountView struct {
	ID             int64  `json:"id"`
	Email          string `json:"email"`
	Name           string `json:"name"`
	ProjectID      string `json:"project_id"`
	UserProjectID  string `json:"user_project_id"`
	UserAgent      string `json:"user_agent"` // 实际发给上游的 UA（设备画像或统一 UA）
	Status         string `json:"status"`
	DisabledReason string `json:"disabled_reason,omitempty"`
	// MaxConcurrency 是单独设置的并发上限，0 表示跟随全局默认值（见列表接口的 default_concurrency）。
	MaxConcurrency int `json:"max_concurrency"`
	// InFlight 是该账号此刻进行中的生成请求数（实时并发占用），取视图时的快照；/live 接口可轮询刷新。
	InFlight   int                     `json:"in_flight"`
	Cooldowns  []cooldownView          `json:"cooldowns"`
	ModelCount int                     `json:"model_count"`
	Models     []antigravity.ModelInfo `json:"models"`
	// ModelsRefreshedAt 是模型目录（含额度）上次刷新的 Unix 毫秒。
	ModelsRefreshedAt int64 `json:"models_refreshed_at"`
	// Subscription 是订阅档位，升级前导入、还没检查过的账号为 null。
	Subscription *subscriptionView `json:"subscription"`
	SyncedAt     int64             `json:"synced_at"`
	CreatedAt    int64             `json:"created_at"`
	// Proxy 是绑定的代理（不含地址和密码），直连为 null；ProxyPausedUntil 是代理连不上后暂停调度的截止
	// （Unix 毫秒），0 表示没有暂停。
	Proxy            *accountProxyView `json:"proxy"`
	ProxyPausedUntil int64             `json:"proxy_paused_until"`
	// WeeklyQuota 是账号自己的每周额度开关（空串跟随全局，on、off），WeeklyQuotaOn 是实际是否拉取。
	// QuotaSummary 是上次拉到的分组额度（含每周额度），没拉过为 null；QuotaSummaryAt 是拉到的 Unix 毫秒。
	WeeklyQuota    string                    `json:"weekly_quota"`
	WeeklyQuotaOn  bool                      `json:"weekly_quota_on"`
	QuotaSummary   *antigravity.QuotaSummary `json:"quota_summary"`
	QuotaSummaryAt int64                     `json:"quota_summary_at"`
}

// accountProxyView 是账号绑定的代理。
type accountProxyView struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	EgressIP      string `json:"egress_ip"`
	EgressCountry string `json:"egress_country"`
	CheckError    string `json:"check_error,omitempty"`
	// Status 是代理的启停：停用时这个账号暂停调度。
	Status string `json:"status"`
}

// accountSnapshot 是构造账号视图用的运行状态和代理，列表接口只取一次。
type accountSnapshot struct {
	cooldowns map[int64][]pool.CooldownInfo
	paused    map[int64]time.Time
	inflight  map[int64]int
	proxies   map[int64]*store.Proxy
}

func (s *Service) accountSnapshot(ctx context.Context) accountSnapshot {
	snap := accountSnapshot{cooldowns: s.relay.Cooldowns(), paused: s.relay.Paused(), inflight: s.relay.InFlight(), proxies: map[int64]*store.Proxy{}}
	proxies, err := s.store.ListProxies(ctx, s.enc)
	if err != nil {
		slog.ErrorContext(ctx, "读取代理失败，账号视图里不显示代理", "err", err)
	}
	for _, p := range proxies {
		snap.proxies[p.ID] = p
	}
	return snap
}

// subscriptionView 是账号的订阅档位和检查时间。
type subscriptionView struct {
	antigravity.Subscription
	// Downgraded 表示付费档位降到了 Antigravity Starter Quota（订阅可能已失效）。
	Downgraded bool  `json:"downgraded"`
	CheckedAt  int64 `json:"checked_at"`
}

// cooldownView 是账号某个模型生效中的冷却。
type cooldownView struct {
	Model  string `json:"model"`
	Until  int64  `json:"until"` // Unix 毫秒
	Reason string `json:"reason"`
}

func (s *Service) toAccountView(ctx context.Context, a *store.Account) accountView {
	return s.toAccountViewWith(a, s.accountSnapshot(ctx))
}

// toAccountViewWith 用给定的快照构造视图，列表接口只取一次快照。
func (s *Service) toAccountViewWith(a *store.Account, snap accountSnapshot) accountView {
	cooldowns := snap.cooldowns
	models := antigravity.UnmarshalModels(a.ModelsJSON)
	var sub *subscriptionView
	if a.SubscriptionJSON != "" {
		parsed := antigravity.UnmarshalSubscription(a.SubscriptionJSON)
		sub = &subscriptionView{Subscription: parsed, Downgraded: parsed.Downgraded(), CheckedAt: a.SubscriptionCheckedAt}
	}
	cds := make([]cooldownView, 0, len(cooldowns[a.ID]))
	for _, c := range cooldowns[a.ID] {
		cds = append(cds, cooldownView{Model: c.Model, Until: c.Until.UnixMilli(), Reason: c.Reason})
	}
	return accountView{
		ID:                a.ID,
		Email:             a.Email,
		Name:              a.Name,
		ProjectID:         a.ProjectID,
		UserProjectID:     a.UserProjectID,
		UserAgent:         s.client.UserAgentFor(a.UserAgent, a.Email),
		Status:            a.Status,
		DisabledReason:    a.DisabledReason,
		MaxConcurrency:    a.MaxConcurrency,
		InFlight:          snap.inflight[a.ID],
		Cooldowns:         cds,
		ModelCount:        len(models),
		Models:            models,
		ModelsRefreshedAt: a.ModelsRefreshedAt,
		Subscription:      sub,
		SyncedAt:          a.SyncedAt,
		CreatedAt:         a.CreatedAt,
		Proxy:             accountProxy(snap.proxies[a.ProxyID]),
		ProxyPausedUntil:  unixMilli(snap.paused[a.ID]),
		WeeklyQuota:       a.WeeklyQuota,
		WeeklyQuotaOn:     s.relay.WeeklyQuotaOn(a),
		QuotaSummary:      antigravity.UnmarshalQuotaSummary(a.QuotaSummaryJSON),
		QuotaSummaryAt:    a.QuotaSummaryAt,
	}
}

// unixMilli 把时间换成 Unix 毫秒，零值为 0。
func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func accountProxy(p *store.Proxy) *accountProxyView {
	if p == nil {
		return nil
	}
	return &accountProxyView{ID: p.ID, Name: p.Name, EgressIP: p.EgressIP, EgressCountry: p.EgressCountry,
		CheckError: p.CheckError, Status: p.Status}
}

func (s *Service) adminListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListAccounts(r.Context(), s.enc)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snap := s.accountSnapshot(r.Context())
	views := make([]accountView, 0, len(accounts))
	for _, a := range accounts {
		views = append(views, s.toAccountViewWith(a, snap))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts":            views,
		"default_concurrency": s.relay.Policy().Concurrency,
		// low_quota 是避让阈值（百分比），剩余额度低于它的模型在管理端标出。
		"low_quota": schedulingFromPolicy(s.relay.Policy()).LowQuota,
		// quota_refresh 是后台刷新额度的间隔（秒，0 为不刷新），管理端据此提示额度可能已过期；
		// weekly_quota 是每周额度的全局开关，账号跟随全局时显示它。
		"quota_refresh": schedulingFromPolicy(s.relay.Policy()).QuotaRefresh,
		"weekly_quota":  s.relay.Policy().WeeklyQuota,
	})
}

// accountSettingsRequest 是账号单独设置的修改，只改给了的字段。
type accountSettingsRequest struct {
	// MaxConcurrency 是并发上限，0 表示跟随全局默认值。
	MaxConcurrency *int `json:"max_concurrency"`
	// WeeklyQuota 是每周额度开关：空串跟随全局，on、off。
	WeeklyQuota *string `json:"weekly_quota"`
}

func (s *Service) adminUpdateAccountSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req accountSettingsRequest
	if !decodeAdminJSON(w, r, 1<<10, &req) {
		return
	}
	if req.MaxConcurrency != nil && (*req.MaxConcurrency < 0 || *req.MaxConcurrency > 64) {
		adminError(w, http.StatusBadRequest, "max_concurrency must be between 0 and 64 (0 follows the global default)")
		return
	}
	if req.WeeklyQuota != nil && *req.WeeklyQuota != "" && *req.WeeklyQuota != store.WeeklyQuotaOn && *req.WeeklyQuota != store.WeeklyQuotaOff {
		adminError(w, http.StatusBadRequest, "weekly_quota must be empty (follow the global setting), on or off")
		return
	}
	if req.MaxConcurrency == nil && req.WeeklyQuota == nil {
		adminError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	var err error
	if req.MaxConcurrency != nil {
		err = s.store.SetAccountMaxConcurrency(r.Context(), id, *req.MaxConcurrency)
	}
	if err == nil && req.WeeklyQuota != nil {
		err = s.store.SetAccountWeeklyQuota(r.Context(), id, *req.WeeklyQuota)
	}
	if errors.Is(err, store.ErrNotFound) {
		adminError(w, http.StatusNotFound, "account not found")
		return
	}
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	attrs := []any{"account_id", id}
	if req.MaxConcurrency != nil {
		attrs = append(attrs, "max_concurrency", *req.MaxConcurrency)
	}
	if req.WeeklyQuota != nil {
		attrs = append(attrs, "weekly_quota", *req.WeeklyQuota)
	}
	slog.InfoContext(r.Context(), "已修改账号设置", attrs...)
	s.reloadAccounts(r.Context())
	a, err := s.store.GetAccount(r.Context(), s.enc, id)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 刚开启每周额度的启用账号立即拉一次，不用等下次后台刷新。
	if req.WeeklyQuota != nil && a.Enabled() && s.relay.WeeklyQuotaOn(a) {
		ctx, cancel := context.WithTimeout(r.Context(), quotaSummaryTimeout)
		if s.relay.SyncQuotaSummary(ctx, a) {
			if fresh, err := s.store.GetAccount(r.Context(), s.enc, id); err == nil {
				a = fresh
			}
		}
		cancel()
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": s.toAccountView(r.Context(), a)})
}

// adminSyncAccount 手动同步账号：刷新令牌、重新取项目和模型目录（含额度）。
// refresh token 已失效（invalid_grant）时和请求链路一样停用账号，原因写法一致，重新授权后自动启用。
func (s *Service) adminSyncAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a, err := s.store.GetAccount(r.Context(), s.enc, id)
	if err != nil {
		adminError(w, http.StatusNotFound, "account not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), syncTimeout)
	defer cancel()
	warning, err := s.syncAccount(ctx, a)
	if err != nil {
		slog.WarnContext(ctx, "同步账号失败", "account_id", id, "err", err)
		if antigravity.IsInvalidGrant(err) {
			s.disableInvalidGrant(r.Context(), id)
		}
		syncError(w, err)
		return
	}
	slog.InfoContext(ctx, "已同步账号", "account_id", id, "email", a.Email, "warning", warning)
	writeJSON(w, http.StatusOK, map[string]any{"account": s.toAccountView(ctx, a), "warning": warning})
}

// syncTimeout 是一次手动同步（刷新令牌加三个控制面调用）的总时限。
const syncTimeout = 60 * time.Second

// quotaSummaryTimeout 是开启每周额度后立即拉一次的时限，超时就等后台刷新。
const quotaSummaryTimeout = 20 * time.Second

// syncError 把同步失败写成管理端直接显示的中文原因（管理端标题里已经有"同步失败"）。
// refresh token 已失效时带 code=invalid_grant，管理端据此提示重新授权。
func syncError(w http.ResponseWriter, err error) {
	switch {
	case antigravity.IsInvalidGrant(err):
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": relay.ReasonInvalidGrant + "，账号已停用",
			"code":  "invalid_grant",
		})
	case errors.Is(err, context.DeadlineExceeded):
		adminError(w, http.StatusGatewayTimeout, "上游 "+strconv.Itoa(int(syncTimeout.Seconds()))+" 秒内没有完成同步，稍后再试")
	case antigravity.IsProxyError(err):
		adminError(w, http.StatusBadGateway, proxyFailure(err))
	default:
		adminError(w, http.StatusBadGateway, "请求上游出错："+err.Error())
	}
}

// disableInvalidGrant 停用 refresh token 已失效的账号（手动停用的也把原因改成失效，重新授权后会自动启用）。
func (s *Service) disableInvalidGrant(ctx context.Context, id int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.SetAccountStatus(ctx, id, store.StatusDisabled, relay.ReasonInvalidGrant); err != nil {
		slog.ErrorContext(ctx, "停用失效账号失败", "account_id", id, "err", err)
		return
	}
	slog.WarnContext(ctx, "已自动停用账号", "account_id", id, "reason", relay.ReasonInvalidGrant)
	s.reloadAccounts(ctx)
}

func (s *Service) adminSetAccountStatus(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := s.store.SetAccountStatus(r.Context(), id, status, ""); err != nil {
			adminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		slog.InfoContext(r.Context(), "已修改账号状态", "account_id", id, "status", status)
		s.reloadAccounts(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Service) adminDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAccount(r.Context(), id); err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已删除账号", "account_id", id)
	s.reloadAccounts(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// adminBatchAccounts 批量启用、停用或删除账号：一条语句落库，号池只重新加载一次。
// 批量同步要请求上游，由管理端逐个调同步接口，不在这里做。
func (s *Service) adminBatchAccounts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string  `json:"action"`
		IDs    []int64 `json:"ids"`
	}
	if !decodeAdminJSON(w, r, 1<<16, &req) {
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 1000 {
		adminError(w, http.StatusBadRequest, "ids must have 1-1000 items")
		return
	}
	var n int64
	var err error
	switch req.Action {
	case "enable":
		n, err = s.store.SetAccountsStatus(r.Context(), req.IDs, store.StatusEnabled)
	case "disable":
		n, err = s.store.SetAccountsStatus(r.Context(), req.IDs, store.StatusDisabled)
	case "delete":
		n, err = s.store.DeleteAccounts(r.Context(), req.IDs)
	default:
		adminError(w, http.StatusBadRequest, "action must be enable, disable or delete")
		return
	}
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已批量处理账号", "action", req.Action, "ids", req.IDs, "affected", n)
	s.reloadAccounts(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"affected": n})
}

// adminTestAccount 用指定账号发一条测试消息（见 relay.Probe），返回结果；检测失败也是 200，结果里说明原因。
// 拿到上游响应的检测记进用量，API Key 为 relay.ProbeKeyID（0），调用方是管理端。
func (s *Service) adminTestAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if !decodeAdminJSON(w, r, 1<<12, &req) {
		return
	}
	res, err := s.relay.Probe(r.Context(), id, relay.Request{Model: strings.TrimSpace(req.Model), ClientIP: clientIP(r), UserAgent: r.UserAgent()})
	switch {
	case errors.Is(err, store.ErrNotFound):
		adminError(w, http.StatusNotFound, "account not found")
		return
	case errors.Is(err, relay.ErrProbeModel):
		adminError(w, http.StatusBadRequest, "账号的模型目录里没有这个模型")
		return
	case err != nil:
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// accountErrorLimit 是账号详情里"最近错误"的条数。
const accountErrorLimit = 10

// adminAccountStats 返回账号详情的统计：since（Unix 毫秒，默认近 24 小时）以来各模型的尝试次数、成功次数、
// 成功尝试的平均耗时，以及最近的失败尝试，按每次上游尝试统计，见 store.AccountModelStats；
// 费用按最终用这个账号的请求合计（见 store.AccountCostSince）。
func (s *Service) adminAccountStats(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			adminError(w, http.StatusBadRequest, "since must be a Unix millisecond timestamp")
			return
		}
		since = n
	}
	models, err := s.store.AccountModelStats(r.Context(), id, since)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	errs, err := s.store.AccountRecentErrors(r.Context(), id, since, accountErrorLimit)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cost, err := s.store.AccountCostSince(r.Context(), id, since)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "recent_errors": errs, "cost_nanos": cost})
}

// --- 概览 ---

func (s *Service) adminOverview(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListAccounts(r.Context(), s.enc)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	keys, err := s.store.ListAPIKeys(r.Context())
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// "近 24 小时"按整点小时算：当前小时加前 23 个小时，按模型统计和按小时趋势用同一个起点，数字对得上。
	since := time.Now().Truncate(time.Hour).Add(-23 * time.Hour).UnixMilli()
	stats, err := s.store.UsageByModel(r.Context(), since)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows, err := s.store.UsageHourly(r.Context(), since)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	enabled := 0
	for _, a := range accounts {
		if a.Enabled() && a.ProjectID != "" {
			enabled++
		}
	}
	keysEnabled := 0
	for _, k := range keys {
		if k.Status == store.StatusEnabled {
			keysEnabled++
		}
	}
	var reqs int64
	for _, st := range stats {
		reqs += st.Requests
	}
	inFlight := 0
	for _, n := range s.relay.InFlight() {
		inFlight += n
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts_total":   len(accounts),
		"accounts_ready":   enabled,
		"api_keys_total":   len(keys),
		"api_keys_enabled": keysEnabled,
		"requests_24h":     reqs,
		"in_flight":        inFlight,
		"usage_by_model":   stats,
		"hourly":           fillHours(since, 24, rows),
	})
}

// adminLive 返回实时并发：各账号当前进行中的生成请求数（账号 ID → 占用）和总数，供账号页和概览页轮询刷新。
// 只读号池内存计数，不读库；now 是服务器时间（Unix 毫秒），前端据此对齐"已更新"的时间。
func (s *Service) adminLive(w http.ResponseWriter, _ *http.Request) {
	inflight := s.relay.InFlight()
	total := 0
	for _, n := range inflight {
		total += n
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"in_flight": inflight,
		"total":     total,
		"now":       time.Now().UnixMilli(),
	})
}

// fillHours 把只含有记录小时的聚合结果补成从 since 起连续 n 个小时，没有记录的小时计 0。
func fillHours(since int64, n int, rows []*store.HourlyUsage) []*store.HourlyUsage {
	out := make([]*store.HourlyUsage, n)
	for i := range out {
		out[i] = &store.HourlyUsage{Start: since + int64(i)*time.Hour.Milliseconds()}
	}
	for _, r := range rows {
		if i := (r.Start - since) / time.Hour.Milliseconds(); i >= 0 && i < int64(n) {
			out[i] = r
		}
	}
	return out
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		adminError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}
