// Package anthropic 在 Anthropic Messages API 和 Gemini 格式之间转换：入站的 Messages 请求转成 Gemini 请求体交给执行层，
// 执行层返回的 Gemini 响应（非流式 JSON、流式 SSE）转回 Messages 响应和流式事件。只做格式转换，不碰网络。
//
// 思考签名：Claude 的签名和思考文字在同一个 part 上，对应 thinking 块的 signature；Gemini 的签名挂在思考之后的
// 工具调用 part 上，Messages 的 tool_use 块没有放签名的位置，所以回给客户端时在 tool_use 前插一个文字为空、
// 只带签名的 thinking 块，客户端下一轮原样传回时再挂回工具调用上（见 ParseRequest 和 Streamer）。
package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Request 是转换后的 Messages 请求。
type Request struct {
	// Model 是客户端请求的模型名（原样）。
	Model  string
	Stream bool
	// Body 是 Gemini 请求体（contents、systemInstruction、tools、toolConfig、generationConfig）。
	Body map[string]any
	// Dropped 是去掉的不支持内容（服务端工具等），记日志用。
	Dropped []string
}

// InvalidError 是请求本身不合法（400）。
type InvalidError struct{ msg string }

func (e *InvalidError) Error() string { return e.msg }

func invalid(format string, args ...any) error { return &InvalidError{fmt.Sprintf(format, args...)} }

// billingHeaderPrefix 是 Claude Code 加在 system 里的计费块：带每次请求的计费信息和指纹，不发给上游。
const billingHeaderPrefix = "x-anthropic-billing-header:"

// ParseRequest 解析 Messages 请求体并转成 Gemini 请求体。countOnly 为真时（count_tokens）不要求 max_tokens。
func ParseRequest(raw []byte, countOnly bool) (*Request, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 工具参数里的大整数不丢精度
	var in map[string]any
	if err := dec.Decode(&in); err != nil {
		return nil, invalid("invalid JSON body")
	}
	model, _ := in["model"].(string)
	if strings.TrimSpace(model) == "" {
		return nil, invalid("model: field required")
	}
	stream, _ := in["stream"].(bool)
	req := &Request{Model: model, Stream: stream}
	c := converter{toolNames: map[string]string{}, req: req}

	body := map[string]any{}
	if sys := c.system(in["system"]); len(sys) > 0 {
		body["systemInstruction"] = map[string]any{"role": "user", "parts": sys}
	}
	msgs, ok := in["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return nil, invalid("messages: at least one message is required")
	}
	contents, err := c.messages(msgs)
	if err != nil {
		return nil, err
	}
	body["contents"] = contents
	if tools := c.tools(in["tools"]); len(tools) > 0 {
		body["tools"] = []any{map[string]any{"functionDeclarations": tools}}
		if tc := toolConfig(in["tool_choice"]); tc != nil {
			body["toolConfig"] = tc
		}
	}
	gen, err := generationConfig(in, countOnly)
	if err != nil {
		return nil, err
	}
	if len(gen) > 0 {
		body["generationConfig"] = gen
	}
	req.Body = body
	return req, nil
}

type converter struct {
	// toolNames 记 tool_use 的 id → 工具名：tool_result 只带 id，Gemini 的 functionResponse 要名字。
	toolNames map[string]string
	req       *Request
}

func (c *converter) drop(what string) { c.req.Dropped = append(c.req.Dropped, what) }

// system 把 system（字符串或文本块数组）转成 Gemini part，去掉 Claude Code 的计费块。
func (c *converter) system(v any) []any {
	var parts []any
	add := func(text string) {
		if strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), billingHeaderPrefix) {
			return
		}
		if text != "" {
			parts = append(parts, map[string]any{"text": text})
		}
	}
	switch s := v.(type) {
	case string:
		add(s)
	case []any:
		for _, b := range s {
			if block, ok := b.(map[string]any); ok && block["type"] == "text" {
				text, _ := block["text"].(string)
				add(text)
			}
		}
	}
	return parts
}

// messages 把消息转成 Gemini contents：user → user，assistant → model。
func (c *converter) messages(msgs []any) ([]any, error) {
	contents := make([]any, 0, len(msgs))
	for i, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			return nil, invalid("messages.%d: must be an object", i)
		}
		role, _ := msg["role"].(string)
		var geminiRole string
		switch role {
		case "user":
			geminiRole = "user"
		case "assistant":
			geminiRole = "model"
		case "system", "developer":
			// Claude Code 会在对话中间插 system 消息（提醒之类）：转成用户消息里的 <system-reminder> 文本，
			// 和它自己放在用户消息里的提醒一个写法（CLIProxyAPI 也这样处理）。
			if parts := c.system(msg["content"]); len(parts) > 0 {
				texts := make([]string, len(parts))
				for k, p := range parts {
					texts[k] = p.(map[string]any)["text"].(string)
				}
				reminder := "<system-reminder>\n" + strings.Join(texts, "\n") + "\n</system-reminder>"
				contents = appendContent(contents, "user", []any{map[string]any{"text": reminder}})
			}
			continue
		default:
			return nil, invalid("messages.%d.role: must be user, assistant or system", i)
		}
		parts, err := c.content(msg["content"], role == "assistant", i)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			continue // 只有被丢弃内容（如不带签名的思考）的消息
		}
		contents = appendContent(contents, geminiRole, parts)
	}
	if len(contents) == 0 {
		return nil, invalid("messages: no content to send")
	}
	return contents, nil
}

// appendContent 追加一条 Gemini 消息；和上一条同一角色时合并进去（system 消息转成用户消息后，可能和前后的用户消息相邻）。
func appendContent(contents []any, role string, parts []any) []any {
	if n := len(contents); n > 0 {
		if last := contents[n-1].(map[string]any); last["role"] == role {
			last["parts"] = append(last["parts"].([]any), parts...)
			return contents
		}
	}
	return append(contents, map[string]any{"role": role, "parts": parts})
}

// content 转一条消息的内容（字符串或内容块数组）。
func (c *converter) content(v any, assistant bool, i int) ([]any, error) {
	if s, ok := v.(string); ok {
		if s == "" {
			return nil, nil
		}
		return []any{map[string]any{"text": s}}, nil
	}
	blocks, ok := v.([]any)
	if !ok {
		return nil, invalid("messages.%d.content: must be a string or an array of content blocks", i)
	}
	var parts []any
	// carrier 是前一个只带签名的 thinking 块（Gemini 的签名），挂到紧接着的工具调用上。
	carrier := ""
	for j, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			return nil, invalid("messages.%d.content.%d: must be an object", i, j)
		}
		typ, _ := block["type"].(string)
		switch typ {
		case "text":
			if text, _ := block["text"].(string); text != "" {
				parts = append(parts, map[string]any{"text": text})
			}
		case "image", "document":
			p, err := sourcePart(block, typ)
			if err != nil {
				return nil, invalid("messages.%d.content.%d: %s", i, j, err)
			}
			if p != nil {
				parts = append(parts, p)
			}
		case "tool_use":
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			if name == "" {
				return nil, invalid("messages.%d.content.%d.name: field required", i, j)
			}
			c.toolNames[id] = name
			call := map[string]any{"name": name, "args": objectOr(block["input"])}
			if id != "" {
				call["id"] = id
			}
			part := map[string]any{"functionCall": call}
			if carrier != "" {
				part["thoughtSignature"] = carrier
				carrier = ""
			}
			parts = append(parts, part)
		case "tool_result":
			p, err := c.toolResult(block)
			if err != nil {
				return nil, invalid("messages.%d.content.%d: %s", i, j, err)
			}
			parts = append(parts, p)
		case "thinking":
			if !assistant {
				continue
			}
			text, _ := block["thinking"].(string)
			sig, _ := block["signature"].(string)
			switch {
			case sig == "":
				c.drop("unsigned thinking block") // 不带签名的思考块会被 Claude 拒绝
			case text == "":
				carrier = sig
			default:
				parts = append(parts, map[string]any{"text": text, "thought": true, "thoughtSignature": sig})
			}
		case "redacted_thinking":
			c.drop("redacted_thinking block")
		default:
			// 服务端工具的调用和结果（server_tool_use、web_search_tool_result 等）、容器上传等，上游没有对应。
			c.drop(typ + " block")
		}
	}
	return parts, nil
}

// sourcePart 转 image、document 块：base64 → inlineData；url → fileData（图片由执行层下载后内联）；
// 文本文档 → 文本。file_id 来源（Files API）不支持。
func sourcePart(block map[string]any, typ string) (map[string]any, error) {
	src, _ := block["source"].(map[string]any)
	if src == nil {
		return nil, errors.New("source: field required")
	}
	switch st, _ := src["type"].(string); st {
	case "base64":
		mime, _ := src["media_type"].(string)
		data, _ := src["data"].(string)
		if mime == "" || data == "" {
			return nil, errors.New("source: media_type and data are required")
		}
		return map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": data}}, nil
	case "url":
		url, _ := src["url"].(string)
		if url == "" {
			return nil, errors.New("source.url: field required")
		}
		mime := "image/*" // 下载时按内容识别真实类型
		if typ == "document" {
			mime = "application/pdf"
		}
		return map[string]any{"fileData": map[string]any{"mimeType": mime, "fileUri": url}}, nil
	case "text":
		if text, _ := src["data"].(string); text != "" {
			return map[string]any{"text": text}, nil
		}
		return nil, nil
	case "content":
		var b strings.Builder
		items, _ := src["content"].([]any)
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && m["type"] == "text" {
				text, _ := m["text"].(string)
				b.WriteString(text)
			}
		}
		if b.Len() == 0 {
			return nil, nil
		}
		return map[string]any{"text": b.String()}, nil
	case "file":
		return nil, errors.New("file sources (Files API) are not supported")
	default:
		return nil, fmt.Errorf("source.type %q is not supported", st)
	}
}

// toolResult 转 tool_result：文本放进 functionResponse.response 的 result（is_error 时放 error），图片和文档
// 放进 functionResponse.parts。
func (c *converter) toolResult(block map[string]any) (map[string]any, error) {
	id, _ := block["tool_use_id"].(string)
	if id == "" {
		return nil, errors.New("tool_use_id: field required")
	}
	name := c.toolNames[id]
	if name == "" {
		name = id // 历史里找不到对应的 tool_use（被客户端裁掉了），用 id 顶上
	}
	var texts []string
	var media []any
	switch v := block["content"].(type) {
	case string:
		texts = append(texts, v)
	case []any:
		for _, it := range v {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			switch m["type"] {
			case "text":
				text, _ := m["text"].(string)
				texts = append(texts, text)
			case "image", "document":
				p, err := sourcePart(m, m["type"].(string))
				if err != nil {
					return nil, err
				}
				if p == nil {
					continue
				}
				if _, isText := p["text"]; isText {
					texts = append(texts, p["text"].(string))
				} else {
					media = append(media, p)
				}
			}
		}
	}
	key := "result"
	if isErr, _ := block["is_error"].(bool); isErr {
		key = "error"
	}
	resp := map[string]any{"id": id, "name": name, "response": map[string]any{key: strings.Join(texts, "\n")}}
	if len(media) > 0 {
		resp["parts"] = media
	}
	return map[string]any{"functionResponse": resp}, nil
}

// tools 转自定义工具为 functionDeclarations；服务端工具（web_search、bash、text_editor、computer 等，
// 带版本号的 type）上游没有对应，去掉。
func (c *converter) tools(v any) []any {
	list, _ := v.([]any)
	var decls []any
	for _, t := range list {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tool["name"].(string)
		typ, _ := tool["type"].(string)
		schema, hasSchema := tool["input_schema"]
		if (typ != "" && typ != "custom") || !hasSchema || name == "" {
			if typ == "" {
				typ = "tool without input_schema"
			}
			c.drop("server tool " + typ)
			continue
		}
		decl := map[string]any{"name": name, "parameters": schema}
		if desc, _ := tool["description"].(string); desc != "" {
			decl["description"] = desc
		}
		decls = append(decls, decl)
	}
	return decls
}

// toolConfig 转 tool_choice：auto → AUTO，any → ANY，tool → ANY 加 allowedFunctionNames，none → NONE。
func toolConfig(v any) map[string]any {
	tc, _ := v.(map[string]any)
	if tc == nil {
		return nil
	}
	var cfg map[string]any
	switch tc["type"] {
	case "auto":
		cfg = map[string]any{"mode": "AUTO"}
	case "any":
		cfg = map[string]any{"mode": "ANY"}
	case "none":
		cfg = map[string]any{"mode": "NONE"}
	case "tool":
		cfg = map[string]any{"mode": "ANY"}
		if name, _ := tc["name"].(string); name != "" {
			cfg["allowedFunctionNames"] = []any{name}
		}
	default:
		return nil
	}
	return map[string]any{"functionCallingConfig": cfg}
}

// generationConfig 转采样参数和思考设置。数字转成 float64：执行层按 float64 读这些字段。
func generationConfig(in map[string]any, countOnly bool) (map[string]any, error) {
	gen := map[string]any{}
	if v, ok := number(in["max_tokens"]); ok {
		if v < 1 {
			return nil, invalid("max_tokens: must be at least 1")
		}
		gen["maxOutputTokens"] = v
	} else if !countOnly {
		return nil, invalid("max_tokens: field required")
	}
	for from, to := range map[string]string{"temperature": "temperature", "top_p": "topP", "top_k": "topK"} {
		if v, ok := number(in[from]); ok {
			gen[to] = v
		}
	}
	if stops, ok := in["stop_sequences"].([]any); ok && len(stops) > 0 {
		gen["stopSequences"] = stops
	}
	effort := ""
	if oc, ok := in["output_config"].(map[string]any); ok {
		effort, _ = oc["effort"].(string)
	}
	thinking, _ := in["thinking"].(map[string]any)
	switch t, _ := thinking["type"].(string); t {
	case "enabled":
		tc := map[string]any{"includeThoughts": true}
		if b, ok := number(thinking["budget_tokens"]); ok {
			tc["thinkingBudget"] = b
		}
		gen["thinkingConfig"] = tc
	case "adaptive", "auto":
		if effort == "" {
			effort = "high"
		}
		gen["thinkingConfig"] = map[string]any{"includeThoughts": true, "thinkingLevel": effortLevel(effort)}
	default:
		if effort != "" {
			gen["thinkingConfig"] = map[string]any{"thinkingLevel": effortLevel(effort)}
		}
	}
	return gen, nil
}

// effortLevel 把 output_config.effort 换成档位名：max 按最高档 xhigh，其余同名（low、medium、high）。
func effortLevel(effort string) string {
	if strings.EqualFold(effort, "max") {
		return "xhigh"
	}
	return strings.ToLower(effort)
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	}
	return 0, false
}

// objectOr 返回工具参数：对象原样，缺省或不是对象时用空对象（functionCall.args 必须是对象）。
func objectOr(v any) any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}
