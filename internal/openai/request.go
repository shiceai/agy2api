// Package openai 在 OpenAI Chat Completions 和 Gemini 格式之间转换：入站的 Chat Completions 请求转成 Gemini 请求体
// 交给执行层，执行层返回的 Gemini 响应（非流式 JSON、流式 SSE）转回 chat.completion 和 chat.completion.chunk。
// 只做格式转换，不碰网络。
//
// 思考内容放在 reasoning_content（DeepSeek 带起来的通行写法，OpenAI 本身没有这个字段，各家客户端大多认它）。
// OpenAI 格式没有放思考签名的位置，签名不回给客户端；下一轮历史里的工具调用由信封补占位签名（见 antigravity 包）。
package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"path"
	"strings"
)

// Request 是转换后的 Chat Completions 请求。
type Request struct {
	// Model 是客户端请求的模型名（原样）。
	Model  string
	Stream bool
	// IncludeUsage 是 stream_options.include_usage：流式最后多发一块用量。
	IncludeUsage bool
	// PromptCacheKey 是 prompt_cache_key（OpenAI 的缓存路由字段），会话粘性用它认对话。
	PromptCacheKey string
	// Body 是 Gemini 请求体（contents、systemInstruction、tools、toolConfig、generationConfig）。
	Body map[string]any
	// Dropped 是去掉的不支持内容，记日志用。
	Dropped []string
}

// InvalidError 是请求本身不合法（400）。
type InvalidError struct{ msg string }

func (e *InvalidError) Error() string { return e.msg }

func invalid(format string, args ...any) error { return &InvalidError{fmt.Sprintf(format, args...)} }

// ParseRequest 解析 Chat Completions 请求体并转成 Gemini 请求体。
func ParseRequest(raw []byte) (*Request, error) {
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
	if opts, ok := in["stream_options"].(map[string]any); ok && stream {
		req.IncludeUsage, _ = opts["include_usage"].(bool)
	}
	if key, ok := in["prompt_cache_key"].(string); ok {
		req.PromptCacheKey = strings.TrimSpace(key)
	}
	if n, ok := number(in["n"]); ok && n != 1 {
		return nil, invalid("n: only 1 is supported")
	}
	msgs, ok := in["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return nil, invalid("messages: at least one message is required")
	}
	c := converter{toolNames: map[string]string{}, req: req}
	system, contents, err := c.messages(msgs)
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
		if tc := toolConfig(in["tool_choice"]); tc != nil {
			body["toolConfig"] = tc
		}
	}
	gen, err := generationConfig(in)
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
	// toolNames 记 tool_calls 的 id → 工具名：tool 消息只带 id，Gemini 的 functionResponse 要名字。
	toolNames map[string]string
	req       *Request
}

func (c *converter) drop(what string) { c.req.Dropped = append(c.req.Dropped, what) }

// messages 把消息转成 Gemini 的 systemInstruction parts 和 contents：开头连续的 system、developer 消息进
// systemInstruction，对话中间的转成用户消息里的 <system-reminder>（和 Anthropic 入站一个写法）；
// user、tool → user，assistant → model。
func (c *converter) messages(msgs []any) (system, contents []any, err error) {
	for i, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			return nil, nil, invalid("messages.%d: must be an object", i)
		}
		switch role, _ := msg["role"].(string); role {
		case "system", "developer":
			texts, err := texts(msg["content"], i)
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
			contents = appendContent(contents, "user", []any{map[string]any{"text": reminder}})
		case "user":
			parts, err := c.userContent(msg["content"], i)
			if err != nil {
				return nil, nil, err
			}
			if len(parts) > 0 {
				contents = appendContent(contents, "user", parts)
			}
		case "assistant":
			parts, err := c.assistant(msg, i)
			if err != nil {
				return nil, nil, err
			}
			if len(parts) > 0 {
				contents = appendContent(contents, "model", parts)
			}
		case "tool":
			part, err := c.toolResult(msg, i)
			if err != nil {
				return nil, nil, err
			}
			contents = appendContent(contents, "user", []any{part})
		default:
			return nil, nil, invalid("messages.%d.role: must be system, developer, user, assistant or tool", i)
		}
	}
	if len(contents) == 0 {
		return nil, nil, invalid("messages: no content to send")
	}
	return system, contents, nil
}

// appendContent 追加一条 Gemini 消息；和上一条同一角色时合并进去（连续的 tool 消息、转成用户消息的 system 消息等）。
func appendContent(contents []any, role string, parts []any) []any {
	if n := len(contents); n > 0 {
		if last := contents[n-1].(map[string]any); last["role"] == role {
			last["parts"] = append(last["parts"].([]any), parts...)
			return contents
		}
	}
	return append(contents, map[string]any{"role": role, "parts": parts})
}

// texts 取消息内容里的文字：字符串，或内容块数组里的 text 块（其它块忽略）。
func texts(v any, i int) ([]string, error) {
	switch c := v.(type) {
	case nil:
		return nil, nil
	case string:
		if c == "" {
			return nil, nil
		}
		return []string{c}, nil
	case []any:
		var out []string
		for _, b := range c {
			if block, ok := b.(map[string]any); ok && block["type"] == "text" {
				if text, _ := block["text"].(string); text != "" {
					out = append(out, text)
				}
			}
		}
		return out, nil
	}
	return nil, invalid("messages.%d.content: must be a string or an array of content parts", i)
}

// userContent 转用户消息的内容：文字、图片、音频、文件。
func (c *converter) userContent(v any, i int) ([]any, error) {
	if _, ok := v.([]any); !ok {
		ts, err := texts(v, i)
		if err != nil || len(ts) == 0 {
			return nil, err
		}
		return []any{map[string]any{"text": ts[0]}}, nil
	}
	var parts []any
	for j, b := range v.([]any) {
		block, ok := b.(map[string]any)
		if !ok {
			return nil, invalid("messages.%d.content.%d: must be an object", i, j)
		}
		var p map[string]any
		var err error
		switch typ, _ := block["type"].(string); typ {
		case "text":
			if text, _ := block["text"].(string); text != "" {
				p = map[string]any{"text": text}
			}
		case "image_url":
			p, err = imagePart(block["image_url"])
		case "input_audio":
			p, err = audioPart(block["input_audio"])
		case "file":
			p, err = filePart(block["file"])
		default:
			c.drop(typ + " content part")
		}
		if err != nil {
			return nil, invalid("messages.%d.content.%d.%s", i, j, err)
		}
		if p != nil {
			parts = append(parts, p)
		}
	}
	return parts, nil
}

// imagePart 转 image_url：data URL → inlineData；http(s) → fileData（由执行层下载后内联）。
func imagePart(v any) (map[string]any, error) {
	var url string
	switch iu := v.(type) {
	case string:
		url = iu
	case map[string]any:
		url, _ = iu["url"].(string)
	}
	switch {
	case url == "":
		return nil, errors.New("image_url.url: field required")
	case strings.HasPrefix(url, "data:"):
		mimeType, data, ok := parseDataURL(url)
		if !ok {
			return nil, errors.New("image_url.url: data URL must be base64 encoded with a media type")
		}
		return inlineData(mimeType, data), nil
	case strings.HasPrefix(url, "http://"), strings.HasPrefix(url, "https://"):
		return map[string]any{"fileData": map[string]any{"mimeType": "image/*", "fileUri": url}}, nil // 下载时按内容识别真实类型
	}
	return nil, errors.New("image_url.url: must be an http(s) URL or a data URL")
}

// audioPart 转 input_audio（base64 数据加格式 wav、mp3）。
func audioPart(v any) (map[string]any, error) {
	in, _ := v.(map[string]any)
	data, _ := in["data"].(string)
	format, _ := in["format"].(string)
	if data == "" || format == "" {
		return nil, errors.New("input_audio: data and format are required")
	}
	return inlineData("audio/"+strings.ToLower(format), data), nil
}

// filePart 转 file：file_data 是 data URL，或纯 base64（类型按 filename 的扩展名）；file_id（Files API）不支持。
func filePart(v any) (map[string]any, error) {
	in, _ := v.(map[string]any)
	if id, _ := in["file_id"].(string); id != "" {
		return nil, errors.New("file.file_id: uploaded files (Files API) are not supported, send file_data instead")
	}
	data, _ := in["file_data"].(string)
	if data == "" {
		return nil, errors.New("file.file_data: field required")
	}
	if strings.HasPrefix(data, "data:") {
		mimeType, b64, ok := parseDataURL(data)
		if !ok {
			return nil, errors.New("file.file_data: data URL must be base64 encoded with a media type")
		}
		return inlineData(mimeType, b64), nil
	}
	name, _ := in["filename"].(string)
	mimeType, _, _ := strings.Cut(mime.TypeByExtension(path.Ext(name)), ";")
	if mimeType == "" {
		return nil, errors.New("file.file_data: use a data URL, or give a filename with a known extension")
	}
	return inlineData(mimeType, data), nil
}

// parseDataURL 拆 data:<类型>;base64,<数据>，只认 base64 编码。
func parseDataURL(s string) (mimeType, data string, ok bool) {
	meta, data, found := strings.Cut(strings.TrimPrefix(s, "data:"), ",")
	mimeType, isBase64 := strings.CutSuffix(meta, ";base64")
	if !found || !isBase64 || mimeType == "" || data == "" {
		return "", "", false
	}
	return mimeType, data, true
}

func inlineData(mimeType, data string) map[string]any {
	return map[string]any{"inlineData": map[string]any{"mimeType": mimeType, "data": data}}
}

// assistant 转助手消息：文字在前、工具调用在后。历史里的 reasoning_content 不带签名，去掉（Claude 会拒绝不带签名的思考）。
func (c *converter) assistant(msg map[string]any, i int) ([]any, error) {
	ts, err := texts(msg["content"], i)
	if err != nil {
		return nil, err
	}
	var parts []any
	for _, t := range ts {
		parts = append(parts, map[string]any{"text": t})
	}
	if rc, _ := msg["reasoning_content"].(string); rc != "" {
		c.drop("reasoning_content in history")
	}
	calls, _ := msg["tool_calls"].([]any)
	for j, tc := range calls {
		call, _ := tc.(map[string]any)
		fn, _ := call["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if name == "" {
			return nil, invalid("messages.%d.tool_calls.%d.function.name: field required", i, j)
		}
		id, _ := call["id"].(string)
		c.toolNames[id] = name
		fc := map[string]any{"name": name, "args": c.arguments(fn["arguments"])}
		if id != "" {
			fc["id"] = id
		}
		parts = append(parts, map[string]any{"functionCall": fc})
	}
	return parts, nil
}

// arguments 把工具调用的 arguments（JSON 字符串）解析成对象；解析不了时用空对象，不让一条坏历史拖垮整个请求。
func (c *converter) arguments(v any) map[string]any {
	s, _ := v.(string)
	if strings.TrimSpace(s) == "" {
		return map[string]any{}
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil || args == nil {
		c.drop("tool call arguments that are not a JSON object")
		return map[string]any{}
	}
	return args
}

// toolResult 转 tool 消息为 functionResponse，文字放进 response.result。
func (c *converter) toolResult(msg map[string]any, i int) (map[string]any, error) {
	id, _ := msg["tool_call_id"].(string)
	if id == "" {
		return nil, invalid("messages.%d.tool_call_id: field required", i)
	}
	name := c.toolNames[id]
	if name == "" {
		name, _ = msg["name"].(string)
	}
	if name == "" {
		name = id // 历史里找不到对应的工具调用（被客户端裁掉了），用 id 顶上
	}
	ts, err := texts(msg["content"], i)
	if err != nil {
		return nil, err
	}
	return map[string]any{"functionResponse": map[string]any{"id": id, "name": name,
		"response": map[string]any{"result": strings.Join(ts, "\n")}}}, nil
}

// tools 转 function 工具为 functionDeclarations，其它类型（内置工具等）上游没有对应，去掉。
// 没有 parameters 时给一个空对象 schema。
func (c *converter) tools(v any) ([]any, error) {
	list, _ := v.([]any)
	var decls []any
	for i, t := range list {
		tool, _ := t.(map[string]any)
		typ, _ := tool["type"].(string)
		fn, _ := tool["function"].(map[string]any)
		if typ != "function" || fn == nil {
			c.drop("tool type " + typ)
			continue
		}
		name, _ := fn["name"].(string)
		if name == "" {
			return nil, invalid("tools.%d.function.name: field required", i)
		}
		params := fn["parameters"]
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		decl := map[string]any{"name": name, "parameters": params}
		if desc, _ := fn["description"].(string); desc != "" {
			decl["description"] = desc
		}
		decls = append(decls, decl)
	}
	return decls, nil
}

// toolConfig 转 tool_choice：auto → AUTO，none → NONE，required → ANY，指定函数 → ANY 加 allowedFunctionNames。
func toolConfig(v any) map[string]any {
	var cfg map[string]any
	switch tc := v.(type) {
	case string:
		switch tc {
		case "auto":
			cfg = map[string]any{"mode": "AUTO"}
		case "none":
			cfg = map[string]any{"mode": "NONE"}
		case "required":
			cfg = map[string]any{"mode": "ANY"}
		}
	case map[string]any:
		fn, _ := tc["function"].(map[string]any)
		if name, _ := fn["name"].(string); tc["type"] == "function" && name != "" {
			cfg = map[string]any{"mode": "ANY", "allowedFunctionNames": []any{name}}
		}
	}
	if cfg == nil {
		return nil
	}
	return map[string]any{"functionCallingConfig": cfg}
}

// generationConfig 转采样参数、思考强度和输出格式。数字转成 float64：执行层按 float64 读这些字段。
func generationConfig(in map[string]any) (map[string]any, error) {
	gen := map[string]any{}
	maxTokens, ok := number(in["max_completion_tokens"])
	if !ok {
		maxTokens, ok = number(in["max_tokens"])
	}
	if ok {
		if maxTokens < 1 {
			return nil, invalid("max_completion_tokens: must be at least 1")
		}
		gen["maxOutputTokens"] = maxTokens
	}
	for from, to := range map[string]string{"temperature": "temperature", "top_p": "topP"} {
		if v, ok := number(in[from]); ok {
			gen[to] = v
		}
	}
	switch stop := in["stop"].(type) {
	case string:
		if stop != "" {
			gen["stopSequences"] = []any{stop}
		}
	case []any:
		if len(stop) > 0 {
			gen["stopSequences"] = stop
		}
	}
	if effort, _ := in["reasoning_effort"].(string); effort != "" {
		level, ok := effortLevel(effort)
		if !ok {
			return nil, invalid("reasoning_effort: must be one of none, minimal, low, medium, high, xhigh")
		}
		gen["thinkingConfig"] = map[string]any{"includeThoughts": true, "thinkingLevel": level}
	}
	if rf, ok := in["response_format"].(map[string]any); ok {
		switch typ, _ := rf["type"].(string); typ {
		case "", "text":
		case "json_object":
			gen["responseMimeType"] = "application/json"
		case "json_schema":
			gen["responseMimeType"] = "application/json"
			js, _ := rf["json_schema"].(map[string]any)
			if schema, ok := js["schema"].(map[string]any); ok {
				gen["responseJsonSchema"] = schema
			}
		default:
			return nil, invalid("response_format.type: must be text, json_object or json_schema")
		}
	}
	return gen, nil
}

// effortLevel 把 reasoning_effort 换成档位名：none、minimal 按最低档 low（上游模型不能完全关掉思考），其余同名。
func effortLevel(effort string) (string, bool) {
	switch e := strings.ToLower(effort); e {
	case "none", "minimal":
		return "low", true
	case "low", "medium", "high", "xhigh":
		return e, true
	}
	return "", false
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
