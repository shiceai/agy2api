package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shiceai/agy2api/internal/relay"
	"github.com/shiceai/agy2api/internal/store"
)

// 被拒上下文拦截："设置 → 请求处理"里开关（默认开）和查看、删除记下的被拒上下文（见 relay/rejected_contexts.go）。

// contextBlockSettings 是被拒上下文拦截的开关。
type contextBlockSettings struct {
	Enabled bool `json:"enabled"`
}

// contextBlockSetting 存在 settings 的 context_block 键，默认开。
var contextBlockSetting = jsonSetting[contextBlockSettings]{
	key:      "context_block",
	name:     "被拒上下文拦截设置",
	limit:    1 << 10,
	defaults: func() contextBlockSettings { return contextBlockSettings{Enabled: true} },
}

// rejectedContextsLimit 是管理端列出的条数上限（最近拦到的在前）。
const rejectedContextsLimit = 100

// rejectedContextView 是一条被拒上下文，带密钥名（密钥已删时为空）。
type rejectedContextView struct {
	store.RejectedContext
	APIKeyName string `json:"api_key_name,omitempty"`
}

// rejectedContextsView 是"请求处理"的被拒上下文卡片：开关、最近的被拒上下文和总条数、保留规则。
type rejectedContextsView struct {
	Settings contextBlockSettings  `json:"settings"`
	Contexts []rejectedContextView `json:"contexts"`
	Total    int                   `json:"total"`
	TTLDays  int                   `json:"ttl_days"`
	Max      int                   `json:"max"`
}

// loadContextBlock 启动时加载开关和记下的被拒上下文。
func (s *Service) loadContextBlock(ctx context.Context) error {
	v, err := contextBlockSetting.load(ctx, s.store)
	if err != nil {
		return err
	}
	s.relay.SetContextBlock(v.Enabled)
	return s.relay.LoadRejectedContexts(ctx)
}

func (s *Service) rejectedContextsView(ctx context.Context) (*rejectedContextsView, error) {
	set, err := contextBlockSetting.load(ctx, s.store)
	if err != nil {
		return nil, err
	}
	list, err := s.store.RejectedContexts(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := s.store.ListAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(keys))
	for _, k := range keys {
		names[k.ID] = k.Name
	}
	v := &rejectedContextsView{Settings: set, Contexts: []rejectedContextView{}, Total: len(list),
		TTLDays: int(relay.RejectedContextTTL.Hours() / 24), Max: relay.RejectedContextMax}
	for i, c := range list {
		if i == rejectedContextsLimit {
			break
		}
		v.Contexts = append(v.Contexts, rejectedContextView{RejectedContext: c, APIKeyName: names[c.APIKeyID]})
	}
	return v, nil
}

func (s *Service) writeRejectedContexts(w http.ResponseWriter, r *http.Request) {
	v, err := s.rejectedContextsView(r.Context())
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) adminGetRejectedContexts(w http.ResponseWriter, r *http.Request) {
	s.writeRejectedContexts(w, r)
}

func (s *Service) adminUpdateContextBlock(w http.ResponseWriter, r *http.Request) {
	v, ok := contextBlockSetting.update(w, r, s.store, contextBlockSetting.defaults())
	if !ok {
		return
	}
	s.relay.SetContextBlock(v.Enabled)
	slog.InfoContext(r.Context(), "已修改被拒上下文拦截设置", "enabled", v.Enabled)
	s.writeRejectedContexts(w, r)
}

// adminDeleteRejectedContext 删除一条被拒上下文，之后包含它的请求照常发往上游。
func (s *Service) adminDeleteRejectedContext(w http.ResponseWriter, r *http.Request) {
	fp := chi.URLParam(r, "fingerprint")
	ok, err := s.store.DeleteRejectedContext(r.Context(), fp)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		adminError(w, http.StatusNotFound, "rejected context not found")
		return
	}
	s.reloadRejectedContexts(r.Context())
	slog.InfoContext(r.Context(), "已删除被拒上下文", "fingerprint", fp)
	s.writeRejectedContexts(w, r)
}

// adminClearRejectedContexts 删除全部被拒上下文（开关不变）。
func (s *Service) adminClearRejectedContexts(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.ClearRejectedContexts(r.Context())
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadRejectedContexts(r.Context())
	slog.InfoContext(r.Context(), "已清空被拒上下文", "deleted", n)
	s.writeRejectedContexts(w, r)
}

// reloadRejectedContexts 删除后让内存里的指纹和库一致。
func (s *Service) reloadRejectedContexts(ctx context.Context) {
	if err := s.relay.LoadRejectedContexts(ctx); err != nil {
		slog.ErrorContext(ctx, "重新读取被拒上下文失败", "err", err)
	}
}
