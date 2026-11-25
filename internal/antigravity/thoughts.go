package antigravity

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
)

// 隐藏思考内容：上游（实测 gemini-2.5-flash）即使请求侧 includeThoughts=false 仍会返回 thought part，
// 所以真正要隐藏得在响应侧把标了 thought:true 的 part 去掉。只在开启"隐藏思考"时才解码重编码，默认不动、保持透传。

// stripThoughtParts 从一帧/一条标准 Gemini 响应里去掉 candidates[].content.parts 里 thought 为真的 part。
// 没有思考 part 时原样返回（保持透传）；usageMetadata（含 thoughtsTokenCount）不动，不影响计费。
func stripThoughtParts(inner []byte) []byte {
	if !hasThoughtPart(inner) {
		return inner
	}
	var doc map[string]any
	if json.Unmarshal(inner, &doc) != nil {
		return inner
	}
	cands, ok := doc["candidates"].([]any)
	if !ok {
		return inner
	}
	for _, c := range cands {
		cand, ok := c.(map[string]any)
		if !ok {
			continue
		}
		content, ok := cand["content"].(map[string]any)
		if !ok {
			continue
		}
		parts, ok := content["parts"].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(parts))
		for _, p := range parts {
			if pm, ok := p.(map[string]any); ok {
				if t, _ := pm["thought"].(bool); t {
					continue
				}
			}
			kept = append(kept, p)
		}
		content["parts"] = kept
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(doc) != nil {
		return inner
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// hasThoughtPart 报告这帧里有没有 thought 为真的 part。
func hasThoughtPart(inner []byte) bool {
	found := false
	gjson.GetBytes(inner, "candidates").ForEach(func(_, c gjson.Result) bool {
		c.Get("content.parts").ForEach(func(_, p gjson.Result) bool {
			if p.Get("thought").Bool() {
				found = true
				return false
			}
			return true
		})
		return !found
	})
	return found
}
