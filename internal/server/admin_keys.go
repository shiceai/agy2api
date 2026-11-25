package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shiceai/agy2api/internal/store"
)

// API Key 管理：新建、修改名称和限制、启停、删除，列表带上过期状态、本周期已用 Token 和近 30 天用量。

const (
	// keyRecentWindow 是列表里"近期用量"的时间范围。
	keyRecentWindow = 30 * 24 * time.Hour
	keyNameMax      = 64
	// maxTokenLimit 只防明显的误输入（约 1 千万亿）。
	maxTokenLimit      = 1_000_000_000_000_000
	keyModelsMax       = 100
	keyModelPatternMax = 128
)

// apiKeyView 是管理端的 Key：名称和限制，加上是否已过期、本周期已用 Token 和下次重置时间（设了上限时；
// 总量没有重置时间，为 0），以及近 30 天的请求数和 Token 数。
type apiKeyView struct {
	*store.APIKey
	Expired       bool              `json:"expired"`
	PeriodUsed    int64             `json:"period_used"`
	PeriodResetAt int64             `json:"period_reset_at"`
	Recent        store.APIKeyUsage `json:"recent"`
}

func (s *Service) keyView(ctx context.Context, k *store.APIKey, recent map[int64]store.APIKeyUsage, now time.Time) (apiKeyView, error) {
	v := apiKeyView{APIKey: k, Expired: keyExpired(k, now), Recent: recent[k.ID]}
	if k.TokenLimit > 0 {
		used, err := s.store.APIKeyTokensSince(ctx, k.ID, periodStart(k.TokenPeriod, now).UnixMilli())
		if err != nil {
			return v, err
		}
		v.PeriodUsed = used
		if end := periodEnd(k.TokenPeriod, now); !end.IsZero() {
			v.PeriodResetAt = end.UnixMilli()
		}
	}
	return v, nil
}

func (s *Service) adminListKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	keys, err := s.store.ListAPIKeys(ctx)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	recent, err := s.store.APIKeysUsageSince(ctx, now.Add(-keyRecentWindow).UnixMilli())
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]apiKeyView, 0, len(keys))
	for _, k := range keys {
		v, err := s.keyView(ctx, k, recent, now)
		if err != nil {
			adminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": views})
}

// apiKeyRequest 是新建、修改 Key 的请求体。
type apiKeyRequest struct {
	Name          string   `json:"name"`
	ExpiresAt     int64    `json:"expires_at"`
	TokenLimit    int64    `json:"token_limit"`
	TokenPeriod   string   `json:"token_period"`
	AllowedModels []string `json:"allowed_models"`
}

// settings 校验并规范化名称和限制：没设上限时清掉周期；模型名去空白、去重（不分大小写）。
func (req apiKeyRequest) settings() (store.APIKeySettings, error) {
	out := store.APIKeySettings{Name: strings.TrimSpace(req.Name), ExpiresAt: req.ExpiresAt, TokenLimit: req.TokenLimit}
	if utf8.RuneCountInString(out.Name) > keyNameMax {
		return out, fmt.Errorf("名称最多 %d 个字", keyNameMax)
	}
	if out.ExpiresAt < 0 {
		return out, errors.New("到期时间不正确")
	}
	if out.TokenLimit < 0 || out.TokenLimit > maxTokenLimit {
		return out, errors.New("填写的 Token 上限不正确")
	}
	if out.TokenLimit > 0 {
		switch req.TokenPeriod {
		case store.TokenPeriodDay, store.TokenPeriodMonth, store.TokenPeriodTotal:
			out.TokenPeriod = req.TokenPeriod
		default:
			return out, errors.New("上限周期应为每天、每月或总量")
		}
	}
	seen := map[string]bool{}
	out.AllowedModels = []string{}
	for _, m := range req.AllowedModels {
		m = strings.TrimSpace(m)
		if m == "" || seen[strings.ToLower(m)] {
			continue
		}
		if len(m) > keyModelPatternMax || strings.IndexFunc(m, unicode.IsSpace) >= 0 {
			return out, fmt.Errorf("模型名不正确：%s", m)
		}
		seen[strings.ToLower(m)] = true
		out.AllowedModels = append(out.AllowedModels, m)
	}
	if len(out.AllowedModels) > keyModelsMax {
		return out, fmt.Errorf("允许的模型最多 %d 个", keyModelsMax)
	}
	return out, nil
}

func (s *Service) adminCreateKey(w http.ResponseWriter, r *http.Request) {
	var req apiKeyRequest
	if !decodeAdminJSON(w, r, 1<<16, &req) {
		return
	}
	settings, err := req.settings()
	if err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return
	}
	plain, key, err := s.store.CreateAPIKey(r.Context(), settings)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已创建 API Key", "key_id", key.ID, "name", key.Name, "prefix", key.Prefix)
	s.reloadKeys(r.Context())
	view, err := s.keyView(r.Context(), key, nil, time.Now())
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 明文仅此一次返回。
	writeJSON(w, http.StatusOK, map[string]any{"key": view, "secret": plain})
}

// adminUpdateKey 修改 Key 的名称和限制，立即生效（重新加载鉴权缓存，Token 用量按新周期重算）。
func (s *Service) adminUpdateKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req apiKeyRequest
	if !decodeAdminJSON(w, r, 1<<16, &req) {
		return
	}
	settings, err := req.settings()
	if err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.UpdateAPIKey(r.Context(), id, settings); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			adminError(w, http.StatusNotFound, "API key not found")
			return
		}
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已修改 API Key", "key_id", id, "name", settings.Name, "expires_at", settings.ExpiresAt,
		"token_limit", settings.TokenLimit, "token_period", settings.TokenPeriod, "allowed_models", settings.AllowedModels)
	s.reloadKeys(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// adminKeyStats 返回 Key 自 since（Unix 毫秒，默认近 24 小时）以来各请求模型的请求数、成功数、Token 和
// 成功请求的平均耗时，按最终请求统计（Key 看到的就是请求，不看换号尝试）。
func (s *Service) adminKeyStats(w http.ResponseWriter, r *http.Request) {
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
	models, err := s.store.APIKeyModelStats(r.Context(), id, since)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// adminModelNames 返回号池里能请求的模型名（Key 的允许模型联想用）：各账号目录里的模型 ID 和家族名，排好序。
func (s *Service) adminModelNames(w http.ResponseWriter, _ *http.Request) {
	models := []string{}
	for _, m := range s.relay.Models() {
		models = append(models, m.ID)
	}
	families := []string{}
	for f := range s.relay.Families() {
		families = append(families, f)
	}
	sort.Strings(models)
	sort.Strings(families)
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "families": families})
}

func (s *Service) adminSetKeyStatus(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := s.store.SetAPIKeyStatus(r.Context(), id, status); err != nil {
			adminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		slog.InfoContext(r.Context(), "已修改 API Key 状态", "key_id", id, "status", status)
		s.reloadKeys(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Service) adminDeleteKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAPIKey(r.Context(), id); err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已删除 API Key", "key_id", id)
	s.reloadKeys(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
