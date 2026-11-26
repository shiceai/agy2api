package openai

// OpenAI Responses API（/v1/responses）的请求转换。只做无状态的部分：网关不存响应，previous_response_id、
// conversation、background 不支持，客户端每轮带上完整对话（Codex 就是这样，store:false）。
//
// 思考签名放在 reasoning 条目的 encrypted_content 里往返（客户端在 include 里要了 reasoning.encrypted_content 时）：
// Claude 的签名和思考文字在一起；Gemini 的签名挂在工具调用上，回给客户端时在工具调用前插一条摘要为空、只带签名的
// reasoning 条目，下一轮传回时再挂回工具调用上（同 Anthropic 入站的空思考块）。
//
// 上游只有函数工具：命名空间工具（namespace）里的函数展开成"命名空间__函数名"，自定义工具（custom，Codex 的
// apply_patch 等自由文本工具）转成只有一个字符串参数 input 的函数；回给客户端时按 ResponsesRequest.Tools 还原。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ResponsesRequest 是转换后的 Responses 请求。
type ResponsesRequest struct {
	// Model 是客户端请求的模型名（原样）。
	Model  string
	Stream bool
	// PromptCacheKey 是 prompt_cache_key，会话粘性用它认对话（Codex 填会话 ID）。
	PromptCacheKey string
	// EncryptedReasoning 是 include 里有 reasoning.encrypted_content：回给客户端的 reasoning 条目带上思考签名。
	EncryptedReasoning bool
	// Tools 记命名空间工具和自定义工具，键是发给上游的函数名；普通函数不记。
	Tools map[string]ToolRef
	// Echo 是响应对象里回显的请求参数（instructions、tools、temperature 等）。
	Echo map[string]any
	// Body 是 Gemini 请求体（contents、systemInstruction、tools、toolConfig、generationConfig）。
	Body map[string]any
	// Dropped 是去掉的不支持内容，记日志用。
	Dropped []string
}

// ToolRef 是客户端声明的工具形态。
type ToolRef struct {
	// Namespace 是所在的命名空间，回给客户端时和 Name 分开放。
	Namespace string
	// Name 是客户端的工具名（不带命名空间）。
	Name string
	// Custom 为真时是自定义工具：上游函数的参数是 {"input": 文本}，回给客户端时是 custom_tool_call。
	Custom bool
}

// responsesEchoDefaults 是响应对象里回显的请求参数和客户端没给时的默认值。
var responsesEchoDefaults = map[string]any{
	"instructions":        nil,
	"max_output_tokens":   nil,
	"metadata":            map[string]any{},
	"parallel_tool_calls": true,
	"reasoning":           map[string]any{"effort": nil, "summary": nil},
	"temperature":         nil,
	"text":                map[string]any{"format": map[string]any{"type": "text"}},
	"tool_choice":         "auto",
	"tools":               []any{},
	"top_p":               nil,
	"truncation":          "disabled",
}

// ParseResponsesRequest 解析 Responses 请求体并转成 Gemini 请求体。
func ParseResponsesRequest(raw []byte) (*ResponsesRequest, error) {
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
	if id, _ := in["previous_response_id"].(string); id != "" {
		return nil, invalid("previous_response_id: stored responses are not supported, send the whole conversation in input")
	}
	if in["conversation"] != nil {
		return nil, invalid("conversation: stored conversations are not supported, send the whole conversation in input")
	}
	if bg, _ := in["background"].(bool); bg {
		return nil, invalid("background: background responses are not supported")
	}
	stream, _ := in["stream"].(bool)
	req := &ResponsesRequest{Model: model, Stream: stream, Tools: map[string]ToolRef{}}
	if key, ok := in["prompt_cache_key"].(string); ok {
		req.PromptCacheKey = strings.TrimSpace(key)
	}
	if include, ok := in["include"].([]any); ok {
		for _, v := range include {
			if v == "reasoning.encrypted_content" {
				req.EncryptedReasoning = true
			}
		}
	}

	var system []any
	switch instr := in["instructions"].(type) {
	case nil:
	case string:
		if instr != "" {
			system = append(system, map[string]any{"text": instr})
		}
	default:
		return nil, invalid("instructions: must be a string")
	}
	c := responsesConverter{req: req, toolNames: map[string]string{}}
	contents, system, err := c.input(in["input"], system)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"contents": contents}
	if len(system) > 0 {
		body["systemInstruction"] = map[string]any{"role": "user", "parts": system}
	}
	tools, err := c.tools(in["tools"])
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		body["tools"] = []any{map[string]any{"functionDeclarations": tools}}
		if tc := responsesToolConfig(in["tool_choice"]); tc != nil {
			body["toolConfig"] = tc
		}
	}
	gen, err := responsesGeneration(in)
	if err != nil {
		return nil, err
	}
	if len(gen) > 0 {
		body["generationConfig"] = gen
	}
	req.Body = body
	req.Echo = map[string]any{}
	for k, def := range responsesEchoDefaults {
		if v, ok := in[k]; ok && v != nil {
			req.Echo[k] = v
		} else {
			req.Echo[k] = def
		}
	}
	return req, nil
}

type responsesConverter struct {
	req *ResponsesRequest
	// toolNames 记 call_id → 上游函数名：工具结果只带 call_id，Gemini 的 functionResponse 要名字。
	toolNames map[string]string
	// carrier 是摘要为空的 reasoning 条目带来的签名，挂到同一轮助手消息里接下来的工具调用上。
	carrier string
}

func (c *responsesConverter) drop(what string) { c.req.Dropped = append(c.req.Dropped, what) }

// qualifiedName 是命名空间工具发给上游的函数名：命名空间__函数名（函数名已带这个前缀时不重复加）。
func qualifiedName(namespace, name string) string {
	if namespace == "" || strings.HasPrefix(name, namespace+"__") {
		return name
	}
	return namespace + "__" + name
}

// input 把 input（字符串或条目数组）转成 Gemini contents：开头连续的 system、developer 消息追加到 systemInstruction，
// 对话中间的转成用户消息里的 <system-reminder>（同 Chat Completions 入站）；user 和工具结果 → user，
// assistant 消息、工具调用、带签名的 reasoning → model。
func (c *responsesConverter) input(v any, system []any) (contents, sys []any, err error) {
	var items []any
	switch in := v.(type) {
	case string:
		if in == "" {
			return nil, nil, invalid("input: must not be empty")
		}
		return []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": in}}}}, system, nil
	case []any:
		items = in
	default:
		return nil, nil, invalid("input: must be a string or an array of input items")
	}
	for i, it := range items {
		item, ok := it.(map[string]any)
		if !ok {
			return nil, nil, invalid("input.%d: must be an object", i)
		}
		typ, _ := item["type"].(string)
		if typ == "" && item["role"] != nil {
			typ = "message" // 简写的消息可以不带 type
		}
		switch typ {
		case "message":
			switch role, _ := item["role"].(string); role {
			case "system", "developer":
				texts, err := c.texts(item["content"], i)
				if err != nil {
					return nil, nil, err
				}
				if len(texts) == 0 {
					continue
				}
				if len(contents) == 0 {
					for _, t := range texts {
						system = append(system, map[string]any{"text": t})
					}
					continue
				}
				reminder := "<system-reminder>\n" + strings.Join(texts, "\n") + "\n</system-reminder>"
				contents = c.add(contents, "user", map[string]any{"text": reminder})
			case "user":
				parts, err := c.userContent(item["content"], i)
				if err != nil {
					return nil, nil, err
				}
				contents = c.add(contents, "user", parts...)
			case "assistant":
				texts, err := c.texts(item["content"], i)
				if err != nil {
					return nil, nil, err
				}
				for _, t := range texts {
					contents = c.add(contents, "model", map[string]any{"text": t})
				}
			default:
				return nil, nil, invalid("input.%d.role: must be system, developer, user or assistant", i)
			}
		case "function_call", "custom_tool_call":
			part, err := c.toolCall(item, typ == "custom_tool_call", i)
			if err != nil {
				return nil, nil, err
			}
			contents = c.add(contents, "model", part)
		case "function_call_output", "custom_tool_call_output":
			parts, err := c.toolOutput(item, i)
			if err != nil {
				return nil, nil, err
			}
			contents = c.add(contents, "user", parts...)
		case "reasoning":
			if part := c.reasoning(item); part != nil {
				contents = c.add(contents, "model", part)
			}
		case "item_reference":
			return nil, nil, invalid("input.%d: item_reference needs stored items, which are not supported; send the items themselves", i)
		default:
			// 内置工具的调用和结果（web_search_call、local_shell_call 等）上游没有对应。
			c.drop(typ + " item")
		}
	}
	if len(contents) == 0 {
		return nil, nil, invalid("input: no content to send")
	}
	return contents, system, nil
}

// add 追加 parts 到 contents（和上一条同一角色时合并）。助手消息里的工具调用挂上 reasoning 条目带来的签名；
// 用户消息开始时没用上的签名作废，不串到下一轮。
func (c *responsesConverter) add(contents []any, role string, parts ...any) []any {
	if len(parts) == 0 {
		return contents
	}
	if role != "model" {
		c.carrier = ""
	} else if c.carrier != "" {
		for _, p := range parts {
			if part := p.(map[string]any); part["functionCall"] != nil {
				part["thoughtSignature"] = c.carrier
				c.carrier = ""
				break
			}
		}
	}
	return appendContent(contents, role, parts)
}

// texts 取消息内容里的文字：字符串，或内容数组里的 input_text、output_text、text 和 refusal（其它内容忽略）。
func (c *responsesConverter) texts(v any, i int) ([]string, error) {
	switch content := v.(type) {
	case nil:
		return nil, nil
	case string:
		if content == "" {
			return nil, nil
		}
		return []string{content}, nil
	case []any:
		var out []string
		for _, b := range content {
			block, _ := b.(map[string]any)
			var text string
			switch typ, _ := block["type"].(string); typ {
			case "input_text", "output_text", "text":
				text, _ = block["text"].(string)
			case "refusal":
				text, _ = block["refusal"].(string)
			default:
				c.drop(typ + " content")
			}
			if text != "" {
				out = append(out, text)
			}
		}
		return out, nil
	}
	return nil, invalid("input.%d.content: must be a string or an array of content parts", i)
}

// userContent 转用户消息的内容：文字、图片、文件。
func (c *responsesConverter) userContent(v any, i int) ([]any, error) {
	blocks, ok := v.([]any)
	if !ok {
		texts, err := c.texts(v, i)
		if err != nil || len(texts) == 0 {
			return nil, err
		}
		return []any{map[string]any{"text": texts[0]}}, nil
	}
	var parts []any
	for j, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			return nil, invalid("input.%d.content.%d: must be an object", i, j)
		}
		p, err := c.contentPart(block)
		if err != nil {
			return nil, invalid("input.%d.content.%d.%s", i, j, err)
		}
		if p != nil {
			parts = append(parts, p)
		}
	}
	return parts, nil
}

// contentPart 转一个输入内容：input_text → 文字，input_image → 图片，input_file → 文件；其它去掉。
func (c *responsesConverter) contentPart(block map[string]any) (map[string]any, error) {
	switch typ, _ := block["type"].(string); typ {
	case "input_text", "output_text", "text":
		if text, _ := block["text"].(string); text != "" {
			return map[string]any{"text": text}, nil
		}
		return nil, nil
	case "input_image":
		if id, _ := block["file_id"].(string); id != "" {
			return nil, invalid("file_id: uploaded files (Files API) are not supported, send image_url instead")
		}
		return imagePart(block["image_url"])
	case "input_file":
		if url, _ := block["file_url"].(string); url != "" {
			return nil, invalid("file_url: file URLs are not supported, send file_data instead")
		}
		return filePart(block)
	default:
		c.drop(typ + " content")
		return nil, nil
	}
}

// toolCall 转 function_call、custom_tool_call 为 functionCall；自定义工具的输入文本放进 args.input。
func (c *responsesConverter) toolCall(item map[string]any, custom bool, i int) (map[string]any, error) {
	name, _ := item["name"].(string)
	if name == "" {
		return nil, invalid("input.%d.name: field required", i)
	}
	namespace, _ := item["namespace"].(string)
	name = qualifiedName(namespace, name)
	id, _ := item["call_id"].(string)
	c.toolNames[id] = name
	var args map[string]any
	if custom {
		input, _ := item["input"].(string)
		args = map[string]any{"input": input}
	} else {
		var ok bool
		if args, ok = decodeArguments(item["arguments"]); !ok {
			c.drop("tool call arguments that are not a JSON object")
		}
	}
	fc := map[string]any{"name": name, "args": args}
	if id != "" {
		fc["id"] = id
	}
	return map[string]any{"functionCall": fc}, nil
}

// toolOutput 转工具结果为 functionResponse：文字放进 response.result，图片和文件放进 parts。
func (c *responsesConverter) toolOutput(item map[string]any, i int) ([]any, error) {
	id, _ := item["call_id"].(string)
	if id == "" {
		return nil, invalid("input.%d.call_id: field required", i)
	}
	name := c.toolNames[id]
	if name == "" {
		name = id // 历史里找不到对应的工具调用（被客户端裁掉了），用 id 顶上
	}
	var texts []string
	var media []any
	switch out := item["output"].(type) {
	case nil:
	case string:
		texts = append(texts, out)
	case []any:
		for j, b := range out {
			block, _ := b.(map[string]any)
			p, err := c.contentPart(block)
			if err != nil {
				return nil, invalid("input.%d.output.%d.%s", i, j, err)
			}
			switch {
			case p == nil:
			case p["text"] != nil:
				texts = append(texts, p["text"].(string))
			default:
				media = append(media, p)
			}
		}
	default:
		return nil, invalid("input.%d.output: must be a string or an array of content parts", i)
	}
	resp := map[string]any{"id": id, "name": name, "response": map[string]any{"result": strings.Join(texts, "\n")}}
	if len(media) > 0 {
		resp["parts"] = media
	}
	return []any{map[string]any{"functionResponse": resp}}, nil
}

// reasoning 转 reasoning 条目：带签名和摘要的 → 带签名的思考 part；只有签名的 → 记下，挂到接下来的工具调用上；
// 没有签名的去掉（Claude 拒绝不带签名的思考，Gemini 不需要传回思考文字）。
func (c *responsesConverter) reasoning(item map[string]any) map[string]any {
	sig, _ := item["encrypted_content"].(string)
	if sig == "" {
		c.drop("reasoning item without encrypted_content")
		return nil
	}
	text := reasoningText(item["summary"], "summary_text")
	if text == "" {
		text = reasoningText(item["content"], "reasoning_text")
	}
	if text == "" {
		c.carrier = sig
		return nil
	}
	return map[string]any{"text": text, "thought": true, "thoughtSignature": sig}
}

// reasoningText 拼 reasoning 条目里 summary（summary_text）或 content（reasoning_text）的文字。
func reasoningText(v any, typ string) string {
	list, _ := v.([]any)
	var texts []string
	for _, b := range list {
		block, _ := b.(map[string]any)
		if text, _ := block["text"].(string); block["type"] == typ && text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// tools 转工具声明为 functionDeclarations：function 原样，namespace 里的函数展开，custom 转成带 input 参数的函数；
// 内置工具（web_search、file_search、local_shell 等）上游没有对应，去掉。
func (c *responsesConverter) tools(v any) ([]any, error) {
	list, _ := v.([]any)
	var decls []any
	for i, t := range list {
		tool, _ := t.(map[string]any)
		switch typ, _ := tool["type"].(string); typ {
		case "function", "custom":
			decl, err := c.declaration(tool, "", fmt.Sprintf("tools.%d", i))
			if err != nil {
				return nil, err
			}
			decls = append(decls, decl)
		case "namespace":
			namespace, _ := tool["name"].(string)
			if namespace == "" {
				return nil, invalid("tools.%d.name: field required", i)
			}
			children, _ := tool["tools"].([]any)
			for j, ch := range children {
				child, _ := ch.(map[string]any)
				if typ, _ := child["type"].(string); typ != "function" && typ != "custom" {
					c.drop("tool type " + typ)
					continue
				}
				decl, err := c.declaration(child, namespace, fmt.Sprintf("tools.%d.tools.%d", i, j))
				if err != nil {
					return nil, err
				}
				decls = append(decls, decl)
			}
		default:
			c.drop("tool type " + typ)
		}
	}
	return decls, nil
}

// declaration 转一个 function 或 custom 工具为 functionDeclaration，命名空间和自定义工具记进 req.Tools。
func (c *responsesConverter) declaration(tool map[string]any, namespace, at string) (map[string]any, error) {
	name, _ := tool["name"].(string)
	if name == "" {
		return nil, invalid("%s.name: field required", at)
	}
	qualified := qualifiedName(namespace, name)
	desc, _ := tool["description"].(string)
	custom := tool["type"] == "custom"
	var params any
	if custom {
		// 自由文本工具：输入格式（如 Lark 语法）写进说明，模型按它写 input。
		if format, _ := tool["format"].(map[string]any); format["type"] == "grammar" {
			if def, _ := format["definition"].(string); def != "" {
				syntax, _ := format["syntax"].(string)
				desc = strings.TrimSpace(desc + "\n\nThe input must follow this " + syntax + " grammar:\n" + def)
			}
		}
		params = map[string]any{"type": "object", "required": []any{"input"},
			"properties": map[string]any{"input": map[string]any{"type": "string", "description": "The raw input text for this tool."}}}
	} else if params = tool["parameters"]; params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if namespace != "" || custom {
		c.req.Tools[qualified] = ToolRef{Namespace: namespace, Name: name, Custom: custom}
	}
	decl := map[string]any{"name": qualified, "parameters": params}
	if desc != "" {
		decl["description"] = desc
	}
	return decl, nil
}

// responsesToolConfig 转 tool_choice：auto、none、required 同 Chat Completions；指定函数或自定义工具 → ANY 加
// allowedFunctionNames。其它写法（allowed_tools、内置工具）按默认处理。
func responsesToolConfig(v any) map[string]any {
	choice, ok := v.(map[string]any)
	if !ok {
		return toolConfig(v)
	}
	name, _ := choice["name"].(string)
	if typ := choice["type"]; (typ != "function" && typ != "custom") || name == "" {
		return nil
	}
	namespace, _ := choice["namespace"].(string)
	return map[string]any{"functionCallingConfig": map[string]any{"mode": "ANY",
		"allowedFunctionNames": []any{qualifiedName(namespace, name)}}}
}

// responsesGeneration 转采样参数、思考强度和输出格式。数字转成 float64：执行层按 float64 读这些字段。
// reasoning.summary 不用转：上游默认就返回思考摘要。
func responsesGeneration(in map[string]any) (map[string]any, error) {
	gen := map[string]any{}
	if n, ok := number(in["max_output_tokens"]); ok {
		if n < 1 {
			return nil, invalid("max_output_tokens: must be at least 1")
		}
		gen["maxOutputTokens"] = n
	}
	for from, to := range map[string]string{"temperature": "temperature", "top_p": "topP"} {
		if v, ok := number(in[from]); ok {
			gen[to] = v
		}
	}
	reasoning, _ := in["reasoning"].(map[string]any)
	if effort, _ := reasoning["effort"].(string); effort != "" {
		level, ok := effortLevel(effort)
		if !ok {
			return nil, invalid("reasoning.effort: must be one of none, minimal, low, medium, high, xhigh")
		}
		gen["thinkingConfig"] = map[string]any{"includeThoughts": true, "thinkingLevel": level}
	}
	text, _ := in["text"].(map[string]any)
	if format, ok := text["format"].(map[string]any); ok {
		switch typ, _ := format["type"].(string); typ {
		case "", "text":
		case "json_object":
			gen["responseMimeType"] = "application/json"
		case "json_schema":
			gen["responseMimeType"] = "application/json"
			if schema, ok := format["schema"].(map[string]any); ok {
				gen["responseJsonSchema"] = schema
			}
		default:
			return nil, invalid("text.format.type: must be text, json_object or json_schema")
		}
	}
	return gen, nil
}

// decodeArguments 把工具调用的 arguments（JSON 字符串）解析成对象；解析不了时返回空对象和 false，
// 不让一条坏历史拖垮整个请求。
func decodeArguments(v any) (map[string]any, bool) {
	s, _ := v.(string)
	if strings.TrimSpace(s) == "" {
		return map[string]any{}, true
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil || args == nil {
		return map[string]any{}, false
	}
	return args, true
}
