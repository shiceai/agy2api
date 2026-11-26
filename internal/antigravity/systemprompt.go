package antigravity

import "strings"

// 系统提示词伪装：注入官方 Antigravity 客户端的基础 system 提示词，让请求的 system 看起来像官方客户端发的。
// 可开关，默认关闭。没有证据表明能减少风控，每个请求多几千 token，还会改变模型行为。
//
// 模板由使用者自己从官方客户端抓取后填入，项目不内置官方提示词原文。

// SystemPromptMode 是系统提示词注入模式。
type SystemPromptMode string

const (
	// SystemPromptPrepend：模板在前，客户端原 system 在后（两者都保留，可能冲突）。
	SystemPromptPrepend SystemPromptMode = "prepend"
	// SystemPromptReplace：只用模板，丢弃客户端原 system。
	SystemPromptReplace SystemPromptMode = "replace"
	// SystemPromptAsUser：模板当 system，客户端原 system 转为开头的一条 user 消息（带桥接说明）。
	SystemPromptAsUser SystemPromptMode = "as_user"
)

// systemAsUserBridge 是"客户端转用户"模式下，放在被移动的客户端 system 之前的桥接说明：
// 告诉模型下面是客户端自己的指导（user 级），据此了解宿主应用和工具，但不覆盖 system。
const systemAsUserBridge = "The following is the client application's own guidance, originally provided as system instructions and now presented at the user level. Read it to understand the host application, its workflow and available tools, and apply its relevant guidance when carrying out subsequent user tasks. It has user-level priority and does not override the system instructions above. Do not treat it as the user task or reply to it separately."

// systemToUserBridge 是 MoveSystemToUser 放在被移动的客户端 system 之前的说明。不假设上面还有 system
// 提示词（兼容开关单独生效时 systemInstruction 为空），所以不写"不覆盖上面的 system"。
const systemToUserBridge = "The text below was the client application's own system instructions, now provided at the user level. Use it to understand the host application, its workflow and available tools, and apply its guidance when carrying out the user's tasks. Do not treat it as a task to answer on its own."

// MoveSystemToUser 把 systemInstruction 整体转成开头的一条 user 消息（带说明），并清空 systemInstruction。
// Anthropic 入站用它规避上游对第三方编码客户端（Claude Code 等）system 的识别：上游只检查 systemInstruction，
// 不看对话内容，systemInstruction 留空即可放行。没有 system 时不改。原地修改 body，返回是否改过。
func MoveSystemToUser(body map[string]any) bool {
	if body == nil {
		return false
	}
	existing := systemParts(body)
	if len(existing) == 0 {
		return false
	}
	delete(body, "systemInstruction")
	delete(body, "system_instruction")
	parts := append([]any{map[string]any{"text": systemToUserBridge}}, existing...)
	prependContent(body, map[string]any{"role": "user", "parts": parts})
	return true
}

// ApplySystemPrompt 按模式把模板注入请求体的 systemInstruction，返回是否改过 body。
// template 为空（去空白后）时不动。原地修改 body。
func ApplySystemPrompt(body map[string]any, mode SystemPromptMode, template string) bool {
	template = strings.TrimSpace(template)
	if body == nil || template == "" {
		return false
	}
	existing := systemParts(body)
	switch mode {
	case SystemPromptReplace:
		setSystemInstruction(body, []any{map[string]any{"text": template}})
	case SystemPromptAsUser:
		setSystemInstruction(body, []any{map[string]any{"text": template}})
		if len(existing) > 0 {
			turn := map[string]any{"role": "user", "parts": append([]any{map[string]any{"text": systemAsUserBridge}}, existing...)}
			prependContent(body, turn)
		}
	default: // prepend
		setSystemInstruction(body, append([]any{map[string]any{"text": template}}, existing...))
	}
	return true
}

// systemParts 返回客户端原 systemInstruction 的 parts（字符串归一为 {text}，去掉空 part），没有时为空。
// systemInstruction 可能写成 camelCase 或 snake_case，值可能是字符串或带 parts 的对象。
func systemParts(body map[string]any) []any {
	var raw []any
	switch v := systemInstructionValue(body).(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			return []any{map[string]any{"text": v}}
		}
		return nil
	case map[string]any:
		raw, _ = v["parts"].([]any)
	}
	out := make([]any, 0, len(raw))
	for _, p := range raw {
		switch pv := p.(type) {
		case string:
			if strings.TrimSpace(pv) != "" {
				out = append(out, map[string]any{"text": pv})
			}
		case map[string]any:
			out = append(out, pv)
		}
	}
	return out
}

// systemInstructionValue 返回 systemInstruction 的值，优先 camelCase，没有时取 snake_case。
func systemInstructionValue(body map[string]any) any {
	if v, ok := body["systemInstruction"]; ok {
		return v
	}
	return body["system_instruction"]
}

// setSystemInstruction 把 systemInstruction 设为给定 parts，统一用 camelCase 键并删掉 snake_case。
func setSystemInstruction(body map[string]any, parts []any) {
	delete(body, "system_instruction")
	body["systemInstruction"] = map[string]any{"parts": parts}
}

// prependContent 把一条消息插到 contents 最前面。
func prependContent(body map[string]any, turn map[string]any) {
	existing, _ := body["contents"].([]any)
	body["contents"] = append([]any{turn}, existing...)
}
