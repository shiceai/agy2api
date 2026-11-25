package antigravity

import (
	"fmt"
	"strings"
)

// normalizeRequest 整理客户端的 Gemini 请求体，供后续转换和上游使用：
//   - snake_case 字段名归一为 camelCase。上游按 proto JSON 解析，两种写法都认，
//     归一是为了让网关自己的处理（思考参数、工具调用签名等）只认一种写法；
//     只改已知是 proto 字段的层级，不递归进 responseSchema、functionCall.args 这类用户数据。
//   - 去掉空 part（除 thought 外没有任何非空值）和因此变空的消息，上游会拒绝空 text。
//   - text 为数组时合并成字符串，为其它非字符串值时转成字符串。
func normalizeRequest(inner map[string]any) {
	camelizeKeys(inner, "system_instruction", "generation_config", "tool_config", "safety_settings", "cached_content")
	if gen, ok := inner["generationConfig"].(map[string]any); ok {
		camelizeAll(gen)
		if tc, ok := gen["thinkingConfig"].(map[string]any); ok {
			camelizeAll(tc)
		}
	}
	if tc, ok := inner["toolConfig"].(map[string]any); ok {
		camelizeAll(tc)
		if fc, ok := tc["functionCallingConfig"].(map[string]any); ok {
			camelizeAll(fc)
		}
	}
	if si, ok := inner["systemInstruction"].(map[string]any); ok {
		if parts, ok := si["parts"]; ok {
			si["parts"] = normalizeParts(parts)
		}
	}
	if contents, ok := inner["contents"]; ok {
		inner["contents"] = normalizeContents(contents)
	}
}

// normalizeContents 整理每条消息的 parts，去掉 parts 为空的消息。不是数组时原样返回。
func normalizeContents(raw any) any {
	contents, ok := raw.([]any)
	if !ok {
		return raw
	}
	out := make([]any, 0, len(contents))
	for _, c := range contents {
		content, ok := c.(map[string]any)
		if !ok {
			out = append(out, c)
			continue
		}
		parts, isList := normalizeParts(content["parts"]).([]any)
		if isList && len(parts) == 0 {
			continue
		}
		if isList {
			content["parts"] = parts
		}
		out = append(out, content)
	}
	return out
}

// normalizeParts 归一 part 字段名、修正 text 类型、去掉空 part。不是数组时原样返回。
func normalizeParts(raw any) any {
	parts, ok := raw.([]any)
	if !ok {
		return raw
	}
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		part, ok := p.(map[string]any)
		if !ok {
			continue
		}
		camelizeAll(part)
		for _, nested := range []string{"inlineData", "fileData"} {
			if m, ok := part[nested].(map[string]any); ok {
				camelizeAll(m)
			}
		}
		if text, ok := part["text"]; ok {
			part["text"] = textValue(text)
		}
		if isEmptyPart(part) {
			continue
		}
		out = append(out, part)
	}
	return out
}

// isEmptyPart 判断 part 除 thought 标记外是否没有任何非空值。
func isEmptyPart(part map[string]any) bool {
	for k, v := range part {
		if k == "thought" {
			continue
		}
		switch t := v.(type) {
		case nil:
		case string:
			if t != "" {
				return false
			}
		case map[string]any:
			if len(t) > 0 {
				return false
			}
		case []any:
			if len(t) > 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// textValue 把 text 字段转成字符串：数组按元素合并（元素可以是字符串或 {"text": ...}），其它值格式化。
func textValue(v any) any {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case []any:
		pieces := make([]string, 0, len(t))
		for _, item := range t {
			switch x := item.(type) {
			case string:
				pieces = append(pieces, x)
			case map[string]any:
				if s, ok := x["text"].(string); ok {
					pieces = append(pieces, s)
				}
			case nil:
			default:
				pieces = append(pieces, fmt.Sprint(x))
			}
		}
		return strings.Join(pieces, "\n")
	default:
		return fmt.Sprint(t)
	}
}

// camelizeKeys 把 m 里列出的 snake_case 键改成 camelCase；两种写法同时存在时保留 camelCase。
func camelizeKeys(m map[string]any, keys ...string) {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		delete(m, k)
		camel := snakeToCamel(k)
		if _, exists := m[camel]; !exists {
			m[camel] = v
		}
	}
}

// camelizeAll 把 m 这一层所有含下划线的键改成 camelCase（不递归）。
func camelizeAll(m map[string]any) {
	var snake []string
	for k := range m {
		if strings.Contains(k, "_") {
			snake = append(snake, k)
		}
	}
	camelizeKeys(m, snake...)
}

func snakeToCamel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}
