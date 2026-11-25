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
	// Template 是注入的 system 提示词，由使用者从官方客户端抓取后填入，项目不内置。
	Template string `json:"template"`
}

// systemPromptSetting 存在 settings 的 system_prompt 键。默认关闭，客户端转用户模式，模板为空。
var systemPromptSetting = jsonSetting[systemPromptView]{
	key:   "system_prompt",
	name:  "系统提示词伪装设置",
	limit: 1 << 20,
	defaults: func() systemPromptView {
		return systemPromptView{Enabled: false, Mode: string(antigravity.SystemPromptAsUser)}
	},
	validate: systemPromptView.validate,
}

// validate 检查模式取值；开启时模板不能为空（空模板什么都不注入，开着没有意义）。
func (v systemPromptView) validate() error {
	switch antigravity.SystemPromptMode(v.Mode) {
	case antigravity.SystemPromptPrepend, antigravity.SystemPromptReplace, antigravity.SystemPromptAsUser:
	default:
		return errors.New("mode must be prepend, replace or as_user")
	}
	if v.Enabled && strings.TrimSpace(v.Template) == "" {
		return errors.New("template is required when enabled")
	}
	return nil
}

// apply 把设置下发给 relay；关闭时清空（不注入）。
func (v systemPromptView) apply(s *Service) {
	if !v.Enabled {
		s.relay.SetSystemPrompt(antigravity.SystemPromptMode(v.Mode), "")
		return
	}
	s.relay.SetSystemPrompt(antigravity.SystemPromptMode(v.Mode), v.Template)
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
	writeJSON(w, http.StatusOK, map[string]any{"settings": v})
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
