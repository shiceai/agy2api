package relay

import (
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/shiceai/agy2api/internal/store"
)

// 失败请求的结构摘要（store.UsageRequestSummary）：从发给上游的 Gemini 请求体里数出轮数、文字长度、图片文件、
// 工具和生成参数，不含任何文字内容，默认对每个上游返回了错误的请求都记，排查上游为什么拒绝。

// generationKeys 是摘要里保留的 generationConfig 字段（数值、短字符串和思考设置）。
var generationKeys = []string{
	"maxOutputTokens", "temperature", "topP", "topK", "candidateCount", "presencePenalty", "frequencyPenalty",
	"seed", "responseMimeType", "responseModalities", "thinkingConfig", "mediaResolution",
}

// summarizeRequest 数出 body（Gemini 请求体）的结构，bytes 是发给上游的请求体（含信封）的字节数。
func summarizeRequest(body map[string]any, bytes int) *store.UsageRequestSummary {
	s := &store.UsageRequestSummary{Bytes: bytes}
	media := map[string]*store.UsageMedia{}
	contents, _ := body["contents"].([]any)
	s.Turns = len(contents)
	for _, c := range contents {
		m, _ := c.(map[string]any)
		if role, _ := m["role"].(string); role != "" {
			if s.Roles == nil {
				s.Roles = map[string]int{}
			}
			s.Roles[role]++
		}
		parts, _ := m["parts"].([]any)
		for _, p := range parts {
			countPart(s, media, p)
		}
	}
	s.SystemChars = systemChars(body)
	for _, mt := range sortedKeys(media) {
		s.Media = append(s.Media, *media[mt])
	}
	tools, _ := body["tools"].([]any)
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		for k, v := range tm {
			if k == "functionDeclarations" || k == "function_declarations" {
				decls, _ := v.([]any)
				s.Tools += len(decls)
			} else {
				s.OtherTools = append(s.OtherTools, k)
			}
		}
	}
	sort.Strings(s.OtherTools)
	if gc, ok := body["generationConfig"].(map[string]any); ok {
		g := map[string]any{}
		for _, k := range generationKeys {
			if v, ok := gc[k]; ok {
				g[k] = v
			}
		}
		for _, k := range []string{"responseSchema", "responseJsonSchema", "response_json_schema", "stopSequences"} {
			if _, ok := gc[k]; ok {
				g[k] = true // 结构化输出、停止词只记有没有
			}
		}
		if len(g) > 0 {
			s.Generation = g
		}
	}
	safety, _ := body["safetySettings"].([]any)
	for _, it := range safety {
		m, _ := it.(map[string]any)
		s.Safety = append(s.Safety, fmt.Sprintf("%v=%v", m["category"], m["threshold"]))
	}
	return s
}

// countPart 把一个 part 计入摘要：文字字符数、内联数据和文件引用（按 MIME 类型）、函数调用和结果。
func countPart(s *store.UsageRequestSummary, media map[string]*store.UsageMedia, p any) {
	pm, ok := p.(map[string]any)
	if !ok {
		if text, ok := p.(string); ok {
			s.TextChars += utf8.RuneCountInString(text)
		}
		return
	}
	if text, ok := pm["text"].(string); ok {
		s.TextChars += utf8.RuneCountInString(text)
	}
	mediaOf := func(mt string) *store.UsageMedia {
		if mt == "" {
			mt = "unknown"
		}
		if media[mt] == nil {
			media[mt] = &store.UsageMedia{MimeType: mt}
		}
		return media[mt]
	}
	if d, ok := firstMap(pm, "inlineData", "inline_data"); ok {
		mt, _ := firstString(d, "mimeType", "mime_type")
		data, _ := d["data"].(string)
		m := mediaOf(mt)
		m.Count++
		m.Bytes += len(data) * 3 / 4 // base64 解码后的大小
	}
	if d, ok := firstMap(pm, "fileData", "file_data"); ok {
		mt, _ := firstString(d, "mimeType", "mime_type")
		mediaOf(mt).Files++
	}
	if _, ok := firstMap(pm, "functionCall", "function_call"); ok {
		s.FunctionCalls++
	}
	if _, ok := firstMap(pm, "functionResponse", "function_response"); ok {
		s.FunctionResponses++
	}
}

// systemChars 数 systemInstruction 的文字字符数（字符串或带 parts 的对象，camelCase 或 snake_case）。
func systemChars(body map[string]any) int {
	v, ok := body["systemInstruction"]
	if !ok {
		v = body["system_instruction"]
	}
	switch sv := v.(type) {
	case string:
		return utf8.RuneCountInString(sv)
	case map[string]any:
		n := 0
		parts, _ := sv["parts"].([]any)
		for _, p := range parts {
			switch pv := p.(type) {
			case string:
				n += utf8.RuneCountInString(pv)
			case map[string]any:
				if text, ok := pv["text"].(string); ok {
					n += utf8.RuneCountInString(text)
				}
			}
		}
		return n
	}
	return 0
}

func firstMap(m map[string]any, keys ...string) (map[string]any, bool) {
	for _, k := range keys {
		if v, ok := m[k].(map[string]any); ok {
			return v, true
		}
	}
	return nil, false
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			return v, true
		}
	}
	return "", false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
