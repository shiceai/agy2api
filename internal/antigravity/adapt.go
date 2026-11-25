package antigravity

import "strings"

// adaptForModel 按上游实测结果调整请求（2026-11-23 用真实账号逐项验证）：
//
//   - Gemini：不支持 presencePenalty、frequencyPenalty（400 "Penalty is not enabled for this model"），去掉；
//     stopSequences 正常生效，保留。
//   - Claude：不支持预填充（400 "This model does not support assistant message prefill"），去掉末尾的
//     模型消息；工具调用历史里 functionCall 必须带 id（400 "tool_use.id: Field required"），补上并与
//     functionResponse 配对。多轮对话不需要占位思考块，不加。
//
// 调用前请求体已经过 normalizeRequest，字段名都是 camelCase。
func adaptForModel(inner map[string]any, p BuildParams) {
	lower := strings.ToLower(p.WireModel)
	switch {
	case strings.HasPrefix(lower, "gemini"):
		if gen, ok := inner["generationConfig"].(map[string]any); ok {
			delete(gen, "presencePenalty")
			delete(gen, "frequencyPenalty")
		}
	case strings.HasPrefix(lower, "claude"):
		dropTrailingModelTurns(inner)
		assignToolCallIDs(inner, conversationSeed(p.AccountID, p.Caller, inner))
	}
}

// dropTrailingModelTurns 去掉 contents 末尾连续的模型消息，让对话以用户消息结尾。
func dropTrailingModelTurns(inner map[string]any) {
	contents, ok := inner["contents"].([]any)
	if !ok {
		return
	}
	end := len(contents)
	for end > 0 {
		c, ok := contents[end-1].(map[string]any)
		if !ok || c["role"] != "model" {
			break
		}
		end--
	}
	inner["contents"] = contents[:end]
}

type pendingCall struct {
	name, id string
}

// assignToolCallIDs 给缺 id 的 functionCall 按出现顺序补上 id，再把之后缺 id 的 functionResponse
// 按名字依次配对到还没回应的调用（名字都对不上时配第一个）。Gemini 协议的客户端通常不带 id，
// 而 Claude 的 tool_use 和 tool_result 要靠 id 对上。id 由对话种子和序号派生（见 toolCallID），
// 同一段历史每次补出的 id 相同，后续轮次追加历史也不改变前面的 id。
func assignToolCallIDs(inner map[string]any, seed string) {
	contents, _ := inner["contents"].([]any)
	var pending []pendingCall
	n := 0
	for _, c := range contents {
		content, ok := c.(map[string]any)
		if !ok {
			continue
		}
		parts, _ := content["parts"].([]any)
		for _, p := range parts {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if call, ok := part["functionCall"].(map[string]any); ok {
				id, _ := call["id"].(string)
				if id == "" {
					n++
					id = toolCallID(seed, n)
					call["id"] = id
				}
				name, _ := call["name"].(string)
				pending = append(pending, pendingCall{name, id})
			}
			if resp, ok := part["functionResponse"].(map[string]any); ok {
				pending = matchResponse(resp, pending)
			}
		}
	}
}

// matchResponse 给 functionResponse 配上调用 id（已有 id 的直接核销），返回剩下还没回应的调用。
func matchResponse(resp map[string]any, pending []pendingCall) []pendingCall {
	if id, _ := resp["id"].(string); id != "" {
		for i, pc := range pending {
			if pc.id == id {
				return append(pending[:i], pending[i+1:]...)
			}
		}
		return pending
	}
	if len(pending) == 0 {
		return pending
	}
	name, _ := resp["name"].(string)
	idx := 0
	for i, pc := range pending {
		if pc.name == name {
			idx = i
			break
		}
	}
	resp["id"] = pending[idx].id
	return append(pending[:idx], pending[idx+1:]...)
}
