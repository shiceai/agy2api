package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shiceai/agy2api/internal/notify"
	"github.com/shiceai/agy2api/internal/relay"
	"github.com/shiceai/agy2api/internal/store"
	"github.com/shiceai/agy2api/internal/upgrade"
)

// 告警通知（"设置 → 通知"）：渠道存在 notify_channels 表（配置加密），哪些事件要通知、实例名称、时区存在
// settings 的 notify 键。事件来自 relay（账号被封、令牌失效、需要验证、没号可用、号池暂停）、代理后台测试和新版本检查，
// 在这里转成通知消息交给 notify.Dispatcher 后台发送。

// notifySetting 存在 settings 的 notify 键。
var notifySetting = jsonSetting[notify.Settings]{
	key:      "notify",
	name:     "通知设置",
	limit:    1 << 12,
	defaults: notify.DefaultSettings,
	validate: notify.Settings.Validate,
}

// maxNotifyChannels 是最多能配几个通知渠道。
const maxNotifyChannels = 10

// 同一件事多久最多通知一次（同一账号、同一模型、同一代理）。
const (
	bannedThrottle     = 24 * time.Hour
	accountThrottle    = 6 * time.Hour
	noAccountThrottle  = time.Hour
	proxyThrottle      = 24 * time.Hour
	newVersionThrottle = 30 * 24 * time.Hour
)

// loadNotify 启动时加载通知设置。
func (s *Service) loadNotify(ctx context.Context) error {
	v, err := notifySetting.load(ctx, s.store)
	if err != nil {
		return err
	}
	s.notifier.SetSettings(v)
	return nil
}

func (s *Service) adminGetNotify(w http.ResponseWriter, r *http.Request) {
	v, err := notifySetting.load(r.Context(), s.store)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": v, "defaults": notifySetting.defaults()})
}

func (s *Service) adminUpdateNotify(w http.ResponseWriter, r *http.Request) {
	base, err := notifySetting.load(r.Context(), s.store)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	v, ok := notifySetting.update(w, r, s.store, base)
	if !ok {
		return
	}
	s.notifier.SetSettings(v)
	slog.InfoContext(r.Context(), "已更新通知设置", "events", v.Events, "timezone", v.Timezone)
	writeJSON(w, http.StatusOK, map[string]any{"settings": v})
}

// notifyChannelView 是通知渠道给管理端的形态：不含 Webhook 地址、密钥和 Token，只给目标说明（Hint）。
type notifyChannelView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Enabled    bool   `json:"enabled"`
	Hint       string `json:"hint"`
	HasSecret  bool   `json:"has_secret"`
	ChatID     string `json:"chat_id,omitempty"`
	LastSentAt int64  `json:"last_sent_at"`
	LastError  string `json:"last_error"`
	CreatedAt  int64  `json:"created_at"`
}

func toNotifyChannelView(c *store.NotifyChannel) notifyChannelView {
	cfg, _ := notify.ParseConfig(c.Config)
	return notifyChannelView{
		ID: c.ID, Name: c.Name, Type: c.Type, Enabled: c.Enabled, Hint: cfg.Hint(c.Type), HasSecret: cfg.Secret != "",
		ChatID: cfg.ChatID, LastSentAt: c.LastSentAt, LastError: c.LastError, CreatedAt: c.CreatedAt,
	}
}

// notifyChannelRequest 是新增、修改渠道的请求。修改时不给的字段保持原值：Webhook 地址、密钥、Token 不回显，
// 管理端留空就是不改。
type notifyChannelRequest struct {
	Name    *string `json:"name"`
	Type    string  `json:"type"`
	Enabled *bool   `json:"enabled"`
	URL     *string `json:"url"`
	Secret  *string `json:"secret"`
	Token   *string `json:"token"`
	ChatID  *string `json:"chat_id"`
}

// applyTo 把请求里给了的配置字段写进 c。
func (q notifyChannelRequest) applyTo(c *notify.Config) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	set(&c.URL, q.URL)
	set(&c.Secret, q.Secret)
	set(&c.Token, q.Token)
	set(&c.ChatID, q.ChatID)
}

func channelName(name *string) (string, error) {
	if name == nil {
		return "", errors.New("请填写名称")
	}
	n := strings.TrimSpace(*name)
	switch {
	case n == "":
		return "", errors.New("请填写名称")
	case utf8.RuneCountInString(n) > 40:
		return "", errors.New("名称最多 40 个字")
	}
	return n, nil
}

func (s *Service) adminListNotifyChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.store.ListNotifyChannels(r.Context(), s.enc)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]notifyChannelView, 0, len(channels))
	for _, c := range channels {
		views = append(views, toNotifyChannelView(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": views})
}

func (s *Service) adminCreateNotifyChannel(w http.ResponseWriter, r *http.Request) {
	var req notifyChannelRequest
	if !decodeAdminJSON(w, r, 1<<14, &req) {
		return
	}
	name, err := channelName(req.Name)
	if err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !notify.ValidType(req.Type) {
		adminError(w, http.StatusBadRequest, "不支持的渠道类型")
		return
	}
	var cfg notify.Config
	req.applyTo(&cfg)
	if err := cfg.Validate(req.Type); err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return
	}
	existing, err := s.store.ListNotifyChannels(r.Context(), s.enc)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(existing) >= maxNotifyChannels {
		adminError(w, http.StatusBadRequest, fmt.Sprintf("最多 %d 个通知渠道", maxNotifyChannels))
		return
	}
	c := &store.NotifyChannel{Name: name, Type: req.Type, Enabled: req.Enabled == nil || *req.Enabled, Config: cfg.String()}
	if err := s.store.CreateNotifyChannel(r.Context(), s.enc, c); err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已添加通知渠道", "channel_id", c.ID, "type", c.Type)
	writeJSON(w, http.StatusOK, map[string]any{"channel": toNotifyChannelView(c)})
}

func (s *Service) adminUpdateNotifyChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.store.GetNotifyChannel(r.Context(), s.enc, id)
	if errors.Is(err, store.ErrNotFound) {
		adminError(w, http.StatusNotFound, "通知渠道不存在")
		return
	} else if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var req notifyChannelRequest
	if !decodeAdminJSON(w, r, 1<<14, &req) {
		return
	}
	if req.Name != nil {
		if c.Name, err = channelName(req.Name); err != nil {
			adminError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Enabled != nil {
		c.Enabled = *req.Enabled
	}
	cfg, err := notify.ParseConfig(c.Config)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.applyTo(&cfg)
	if err := cfg.Validate(c.Type); err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return
	}
	c.Config = cfg.String()
	if err := s.store.UpdateNotifyChannel(r.Context(), s.enc, c); err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已修改通知渠道", "channel_id", c.ID, "enabled", c.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"channel": toNotifyChannelView(c)})
}

func (s *Service) adminDeleteNotifyChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteNotifyChannel(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		adminError(w, http.StatusNotFound, "通知渠道不存在")
		return
	} else if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已删除通知渠道", "channel_id", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// adminTestNotifyChannel 立即给渠道发一条测试消息（停用的渠道也能测），失败时返回可读的原因。
func (s *Service) adminTestNotifyChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.store.GetNotifyChannel(r.Context(), s.enc, id)
	if errors.Is(err, store.ErrNotFound) {
		adminError(w, http.StatusNotFound, "通知渠道不存在")
		return
	} else if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.notifier.Test(ctx, c); err != nil {
		adminError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// onRelayEvent 把 relay 的事件转成通知。relay 在请求路径上同步调用，这里另起 goroutine 读账号信息，不拖慢请求。
func (s *Service) onRelayEvent(e relay.Event) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if m, ok := s.relayEventMessage(ctx, e); ok {
			s.notifier.Notify(m)
		}
	}()
}

func (s *Service) relayEventMessage(ctx context.Context, e relay.Event) (notify.Message, bool) {
	key := func(prefix string, v any) string { return fmt.Sprintf("%s:%v", prefix, v) }
	switch e.Type {
	case relay.EventAccountBanned:
		account, egress := s.notifyAccount(ctx, e.AccountID)
		return notify.Message{
			Event: notify.EventAccountBanned, Title: "账号被条款封禁", At: e.At,
			Fields: []notify.Field{
				{Label: "账号", Value: account},
				{Label: "代理出口", Value: egress},
				{Label: "处理", Value: "账号已自动停用"},
				{Label: "申诉", Value: e.Link},
			},
			Key: key(e.Type, e.AccountID), Throttle: bannedThrottle,
		}, true
	case relay.EventTokenInvalid:
		account, _ := s.notifyAccount(ctx, e.AccountID)
		return notify.Message{
			Event: notify.EventTokenInvalid, Title: "账号的 refresh token 已失效", At: e.At,
			Fields: []notify.Field{
				{Label: "账号", Value: account},
				{Label: "处理", Value: "账号已自动停用，在管理端重新授权后恢复"},
			},
			Key: key(e.Type, e.AccountID), Throttle: accountThrottle,
		}, true
	case relay.EventValidationRequired:
		account, _ := s.notifyAccount(ctx, e.AccountID)
		return notify.Message{
			Event: notify.EventValidationRequired, Title: "账号需要验证", At: e.At,
			Fields: []notify.Field{
				{Label: "账号", Value: account},
				{Label: "模型", Value: e.Model},
				{Label: "处理", Value: "这个模型在该账号上暂停使用；用该账号登录浏览器完成验证"},
				{Label: "验证", Value: e.Link},
			},
			Key: key(e.Type, e.AccountID), Throttle: accountThrottle,
		}, true
	case relay.EventNoAccount:
		recovery := notify.Field{Label: "最早恢复", Time: e.RetryAt}
		if e.RetryAt.IsZero() {
			recovery.Value = "不确定，账号可能都已停用"
		}
		return notify.Message{
			Event: notify.EventNoAccount, Title: "没有可用账号", At: e.At,
			Fields: []notify.Field{
				{Label: "模型", Value: e.Model},
				recovery,
				{Label: "影响", Value: "这个模型的请求返回 429 或 503"},
			},
			Key: key(e.Type, e.Model), Throttle: noAccountThrottle,
		}, true
	case relay.EventPoolPaused:
		account, _ := s.notifyAccount(ctx, e.AccountID)
		return notify.Message{
			Event: notify.EventPoolPause, Title: "号池已暂停", At: e.At,
			Fields: []notify.Field{
				{Label: "原因", Value: "账号 " + account + " 被条款封禁"},
				{Label: "恢复时间", Time: e.Until},
				{Label: "影响", Value: "暂停期间所有请求返回 503，可在管理端提前恢复"},
			},
		}, true
	case relay.EventPoolResumed:
		how := "暂停到时自动恢复"
		if e.Manual {
			how = "管理端手动恢复"
		}
		return notify.Message{
			Event: notify.EventPoolPause, Title: "号池已恢复", At: e.At,
			Fields: []notify.Field{{Label: "方式", Value: how}},
		}, true
	}
	return notify.Message{}, false
}

// notifyAccount 返回通知里账号的写法（邮箱打码）和代理出口；读不到账号时给账号 ID。
func (s *Service) notifyAccount(ctx context.Context, id int64) (account, egress string) {
	a, err := s.store.GetAccount(ctx, s.enc, id)
	if err != nil {
		return fmt.Sprintf("#%d", id), ""
	}
	account = notify.AccountLabel(a.Name, a.Email)
	egress = "直连"
	if a.ProxyID != 0 {
		egress = "未知"
		if p, err := s.store.GetProxy(ctx, s.enc, a.ProxyID); err == nil && p.EgressIP != "" {
			egress = p.EgressIP
			if p.EgressCountry != "" {
				egress += "（" + p.EgressCountry + "）"
			}
		}
	}
	return account, egress
}

// notifyProxyFailed 在后台测试发现原来正常的代理测试失败时通知。
func (s *Service) notifyProxyFailed(p *store.Proxy, reason string) {
	s.notifier.Notify(notify.Message{
		Event: notify.EventProxyFailed, Title: "代理测试失败",
		Fields: []notify.Field{
			{Label: "代理", Value: proxyLabel(p)},
			{Label: "原因", Value: reason},
			{Label: "影响", Value: "绑定它的账号请求会失败，并暂停调度"},
		},
		Key: fmt.Sprintf("proxy_failed:%d", p.ID), Throttle: proxyThrottle,
	})
}

// notifyNewVersion 在查到新版本时通知（每个版本一次）。
func (s *Service) notifyNewVersion(rel upgrade.Release) {
	s.notifier.Notify(notify.Message{
		Event: notify.EventNewVersion, Title: "有新版本",
		Fields: []notify.Field{
			{Label: "新版本", Value: rel.Version},
			{Label: "当前版本", Value: s.build.Version},
			{Label: "发布说明", Value: rel.URL},
		},
		Key: "new_version:" + rel.Version, Throttle: newVersionThrottle,
	})
}

// poolPauseView 是全池暂停给管理端的形态（实时接口带着，概览和账号页顶部提示）。
type poolPauseView struct {
	Since     int64  `json:"since"`
	Until     int64  `json:"until"`
	AccountID int64  `json:"account_id"`
	Account   string `json:"account"` // 触发暂停的账号：名称，没有名称时是邮箱
}

// currentPoolPause 返回生效中的全池暂停，没有时为 nil。只在暂停中才读库（取账号名称）。
func (s *Service) currentPoolPause(ctx context.Context) *poolPauseView {
	p := s.relay.PoolPause()
	if p == nil {
		return nil
	}
	v := &poolPauseView{Since: p.Since.UnixMilli(), Until: p.Until.UnixMilli(), AccountID: p.AccountID}
	if a, err := s.store.GetAccount(ctx, s.enc, p.AccountID); err == nil {
		v.Account = a.Name
		if v.Account == "" {
			v.Account = a.Email
		}
	}
	return v
}

// adminResumePool 提前结束全池暂停。
func (s *Service) adminResumePool(w http.ResponseWriter, r *http.Request) {
	resumed := s.relay.ResumePool(r.Context())
	if resumed {
		slog.InfoContext(r.Context(), "管理端提前恢复了号池")
	}
	writeJSON(w, http.StatusOK, map[string]any{"resumed": resumed})
}
