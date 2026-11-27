package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/shiceai/agy2api/internal/antigravity"
)

// systemPromptView 是系统提示词伪装设置的读写形态。
type systemPromptView struct {
	Enabled bool `json:"enabled"`
	// Mode 是 prepend（前置）、replace（替换）或 as_user（客户端转用户）。
	Mode string `json:"mode"`
	// Template 是自定义的 system 提示词；为空时用内置的官方基线提示词（见 antigravity.BuiltinSystemPrompt）。
	Template string `json:"template"`
}

// systemPromptSetting 存在 settings 的 system_prompt 键。默认开启、客户端转用户模式、模板为空（用内置官方基线），
// 让请求的 system 默认就像官方客户端发的。
var systemPromptSetting = jsonSetting[systemPromptView]{
	key:   "system_prompt",
	name:  "系统提示词伪装设置",
	limit: 1 << 20,
	defaults: func() systemPromptView {
		return systemPromptView{Enabled: true, Mode: string(antigravity.SystemPromptAsUser)}
	},
	validate: systemPromptView.validate,
}

// validate 只检查模式取值：模板可以为空，为空时用内置官方基线，三种模式都能用。
func (v systemPromptView) validate() error {
	switch antigravity.SystemPromptMode(v.Mode) {
	case antigravity.SystemPromptPrepend, antigravity.SystemPromptReplace, antigravity.SystemPromptAsUser:
	default:
		return errors.New("mode must be prepend, replace or as_user")
	}
	return nil
}

// template 返回实际注入的模板：填了自定义就用自定义，否则用内置官方基线。
func (v systemPromptView) template() string {
	if strings.TrimSpace(v.Template) != "" {
		return v.Template
	}
	return antigravity.BuiltinSystemPrompt
}

// apply 把设置下发给 relay；关闭时传空模式清掉。
func (v systemPromptView) apply(s *Service) {
	if !v.Enabled {
		s.relay.SetSystemPrompt("", "")
		return
	}
	s.relay.SetSystemPrompt(antigravity.SystemPromptMode(v.Mode), v.template())
}

// loadSystemPrompt 启动时从库加载系统提示词伪装设置并生效。
func (s *Service) loadSystemPrompt(ctx context.Context) error {
	v, err := systemPromptSetting.load(ctx, s.store)
	if err != nil {
		return err
	}
	v.apply(s)
	return nil
}

func (s *Service) adminGetSystemPrompt(w http.ResponseWriter, r *http.Request) {
	v, err := systemPromptSetting.load(r.Context(), s.store)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// builtin 是内置官方基线原文，供管理端在"来源：内置"时预览；它是只读常量，不随设置保存。
	writeJSON(w, http.StatusOK, map[string]any{"settings": v, "builtin": antigravity.BuiltinSystemPrompt})
}

func (s *Service) adminUpdateSystemPrompt(w http.ResponseWriter, r *http.Request) {
	v, ok := systemPromptSetting.update(w, r, s.store, systemPromptSetting.defaults())
	if !ok {
		return
	}
	v.apply(s)
	slog.InfoContext(r.Context(), "已修改系统提示词伪装设置", "enabled", v.Enabled, "mode", v.Mode, "template_len", len(v.Template))
	s.adminGetSystemPrompt(w, r)
}
