package server

import (
	"context"
	"log/slog"
	"net/http"
)

// 客户端兼容（设置键、管理接口沿用最早的名字 anthropic_compat）。上游会识别第三方编码客户端（Claude Code、
// Codex 等）写在 systemInstruction 里的提示词，返回伪装成额度耗尽的 429（2026-11-25 实测：与措辞无关、遍布整篇，
// 删改单段无效；但上游只看 systemInstruction，不看对话内容）。把客户端的 system 整体降为开头的一条 user 消息、
// systemInstruction 留空即可放行（见 antigravity.MoveSystemToUser）。默认开启，否则这些客户端直接接入会一直 429。
// 代价：客户端 system 从 system 级降到 user 级，可能轻微改变模型遵循指令的方式。作用于 Anthropic 入站和
// OpenAI Responses 入站（编码客户端走这两个），不影响 Gemini、Chat Completions 接口。与全局的"系统提示词伪装"
// 叠加时，本项先把客户端 system 降为用户消息，之后伪装再按自己的模式处理（此时 systemInstruction 已空）。

// anthropicCompatView 是客户端兼容设置。
type anthropicCompatView struct {
	// SystemAsUser 为真时把客户端 system 降为开头的一条用户消息（见上）。
	SystemAsUser bool `json:"system_as_user"`
}

// anthropicCompatSetting 存在 settings 的 anthropic_compat 键，默认开启。
var anthropicCompatSetting = jsonSetting[anthropicCompatView]{
	key:   "anthropic_compat",
	name:  "客户端兼容设置",
	limit: 1 << 10,
	defaults: func() anthropicCompatView {
		return anthropicCompatView{SystemAsUser: true}
	},
}

// loadAnthropicCompat 启动时从库加载客户端兼容设置并生效。
func (s *Service) loadAnthropicCompat(ctx context.Context) error {
	v, err := anthropicCompatSetting.load(ctx, s.store)
	if err != nil {
		return err
	}
	s.clientSystemAsUser.Store(v.SystemAsUser)
	return nil
}

func (s *Service) adminGetAnthropicCompat(w http.ResponseWriter, r *http.Request) {
	v, err := anthropicCompatSetting.load(r.Context(), s.store)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": v})
}

func (s *Service) adminUpdateAnthropicCompat(w http.ResponseWriter, r *http.Request) {
	v, ok := anthropicCompatSetting.update(w, r, s.store, anthropicCompatSetting.defaults())
	if !ok {
		return
	}
	s.clientSystemAsUser.Store(v.SystemAsUser)
	slog.InfoContext(r.Context(), "已修改客户端兼容设置", "system_as_user", v.SystemAsUser)
	s.adminGetAnthropicCompat(w, r)
}
