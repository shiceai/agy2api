package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/shiceai/agy2api/internal/antigravity"
)

// clientFilterView 是客户端名称过滤设置的读写形态。
type clientFilterView struct {
	Enabled bool `json:"enabled"`
	// Mode 是 rewrite（替换）或 block（拦截）。
	Mode string `json:"mode"`
	// UseDefaultKeywords 为真时在自定义规则之外加上预置的客户端名称列表。
	UseDefaultKeywords bool `json:"use_default_keywords"`
	// Rules 是自定义规则文本（每行一条 `查找词=>替换词`，无 => 表示删除，# 注释）。
	Rules string `json:"rules"`
}

// clientFilterSetting 存在 settings 的 client_filter 键。默认关闭，替换模式，启用预置规则。
var clientFilterSetting = jsonSetting[clientFilterView]{
	key:   "client_filter",
	name:  "客户端名称过滤设置",
	limit: 1 << 16,
	defaults: func() clientFilterView {
		return clientFilterView{Enabled: false, Mode: string(antigravity.FilterRewrite), UseDefaultKeywords: true}
	},
	validate: clientFilterView.validate,
}

// filter 按设置编译过滤器；关闭或无有效规则时返回 nil（不过滤）。
func (v clientFilterView) filter() *antigravity.ClientFilter {
	if !v.Enabled {
		return nil
	}
	f := antigravity.NewClientFilter(antigravity.FilterMode(v.Mode), antigravity.ParseFilterRules(v.Rules), v.UseDefaultKeywords)
	if f.Empty() {
		return nil
	}
	return f
}

// validate 检查模式取值。
func (v clientFilterView) validate() error {
	if v.Mode != string(antigravity.FilterRewrite) && v.Mode != string(antigravity.FilterBlock) {
		return errors.New("mode must be rewrite or block")
	}
	return nil
}

// loadClientFilter 启动时从库加载客户端名称过滤设置并生效。
func (s *Service) loadClientFilter(ctx context.Context) error {
	v, err := clientFilterSetting.load(ctx, s.store)
	if err != nil {
		return err
	}
	s.relay.SetClientFilter(v.filter())
	return nil
}

func (s *Service) adminGetClientFilter(w http.ResponseWriter, r *http.Request) {
	v, err := clientFilterSetting.load(r.Context(), s.store)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": v,
		// default_keywords 让管理端展示预置清单。
		"default_keywords": antigravity.DefaultClientKeywords,
	})
}

func (s *Service) adminUpdateClientFilter(w http.ResponseWriter, r *http.Request) {
	v, ok := clientFilterSetting.update(w, r, s.store, clientFilterSetting.defaults())
	if !ok {
		return
	}
	s.relay.SetClientFilter(v.filter())
	slog.InfoContext(r.Context(), "已修改客户端名称过滤设置", "enabled", v.Enabled, "mode", v.Mode, "use_defaults", v.UseDefaultKeywords)
	s.adminGetClientFilter(w, r)
}
