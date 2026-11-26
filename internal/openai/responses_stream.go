package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// skipThoughtSignature 是网关自己补的占位签名（见 antigravity 包），不是上游给的，不回给客户端。
const skipThoughtSignature = "skip_thought_signature_validator"

// ResponsesStreamer 把 Gemini 响应转成 Responses API 的输出条目：思考 → reasoning（文字放 summary），
// 正文 → message，工具调用 → function_call 或 custom_tool_call。流式时按官方顺序发事件：response.created、
// response.in_progress，每个条目 output_item.added、增量、output_item.done，最后 response.completed
// （输出上限、内容过滤时是 response.incomplete，上游出错是 response.failed）。非流式（w 为 nil）不发事件，
// 只拼条目，见 ConvertResponsesResponse。
type ResponsesStreamer struct {
	w       io.Writer
	flush   func()
	req     *ResponsesRequest
	id      string
	created int64
	seq     int

	output   []any
	cur      *outputItem // 正在输出的 reasoning 或 message 条目
	usage    *geminiUsage
	finish   string
	started  bool
	finished bool
	// err 是写客户端失败的错误，之后不再写。
	err error
}

// outputItem 是正在输出的 reasoning 或 message 条目。
type outputItem struct {
	kind  string // reasoning、message
	index int
	id    string
	text  strings.Builder
	sig   string
	// partOpen 是已经发了 summary 或 content 的 part.added。
	partOpen bool
}

// NewResponsesStreamer 创建流式转换器；flush 在每个事件后调用（把数据推给客户端），可为 nil。
func NewResponsesStreamer(w io.Writer, flush func(), req *ResponsesRequest) *ResponsesStreamer {
	return &ResponsesStreamer{w: w, flush: flush, req: req, id: "resp_" + randomString(32), created: time.Now().Unix()}
}

// ConvertResponsesResponse 把非流式 Gemini 响应转成 Responses 的响应对象。
func ConvertResponsesResponse(raw []byte, req *ResponsesRequest) ([]byte, error) {
	var g geminiResponse
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, err
	}
	s := NewResponsesStreamer(nil, nil, req)
	s.process(g)
	s.closeItem()
	status, details := s.outcome()
	return json.Marshal(s.response(status, details, nil, true))
}

// Err 返回写客户端时的错误（客户端断开等）。
func (s *ResponsesStreamer) Err() error { return s.err }

// Chunk 处理一块 Gemini 流式响应（SSE data 的内容）。
func (s *ResponsesStreamer) Chunk(raw []byte) {
	var g geminiResponse
	if err := json.Unmarshal(raw, &g); err != nil {
		return
	}
	s.start()
	s.process(g)
}

func (s *ResponsesStreamer) process(g geminiResponse) {
	if g.UsageMetadata != nil {
		s.usage = g.UsageMetadata
	}
	if len(g.Candidates) == 0 {
		if g.PromptFeedback != nil && g.PromptFeedback.BlockReason != "" {
			s.finish = "SAFETY"
		}
		return
	}
	cand := g.Candidates[0]
	for _, p := range cand.Content.Parts {
		sig := p.ThoughtSignature
		if sig == skipThoughtSignature {
			sig = ""
		}
		switch {
		case p.Thought:
			if p.Text != "" {
				s.reasoningDelta(p.Text)
			}
			if sig == "" {
				continue
			}
			// 签名在思考的最后一块上（Claude）。没有正在输出的思考、客户端又不要签名时不出空条目。
			if s.cur == nil || s.cur.kind != "reasoning" {
				if !s.req.EncryptedReasoning {
					continue
				}
				s.open("reasoning")
			}
			s.cur.sig = sig
			s.closeItem()
		case p.FunctionCall != nil:
			s.closeItem()
			if sig != "" && s.req.EncryptedReasoning {
				// Gemini 的签名挂在工具调用上：插一条只带签名的 reasoning，客户端下一轮传回时挂回去。
				s.open("reasoning").sig = sig
				s.closeItem()
			}
			s.toolCall(p)
		case p.Text != "":
			s.messageDelta(p.Text)
		}
	}
	if cand.FinishReason != "" {
		s.finish = cand.FinishReason
	}
}

// Finish 结束流：关掉正在输出的条目，发 response.completed（或 response.incomplete）。
func (s *ResponsesStreamer) Finish() {
	if s.finished {
		return
	}
	s.start()
	s.closeItem()
	s.finished = true
	status, details := s.outcome()
	typ := "response.completed"
	if status == "incomplete" {
		typ = "response.incomplete"
	}
	s.emit(typ, map[string]any{"response": s.response(status, details, nil, true)})
}

// Error 在流里发 response.failed（流已经开始，状态码改不了了），之后不再输出。
func (s *ResponsesStreamer) Error(status int, message string) {
	if s.finished {
		return
	}
	s.start()
	s.closeItem()
	s.finished = true
	failure := map[string]any{"code": failedCode(status), "message": message}
	s.emit("response.failed", map[string]any{"response": s.response("failed", nil, failure, true)})
}

// failedCode 是 response.failed 里的错误码（Codex 按它决定是否重试：rate_limit_exceeded、server_error 会重试）。
func failedCode(status int) string {
	switch {
	case status == 429:
		return "rate_limit_exceeded"
	case status >= 400 && status < 500:
		return "invalid_prompt"
	}
	return "server_error"
}

// outcome 按上游的 finishReason 给出响应状态：输出上限、内容过滤是 incomplete，其余 completed。
func (s *ResponsesStreamer) outcome() (string, map[string]any) {
	switch {
	case s.finish == "MAX_TOKENS":
		return "incomplete", map[string]any{"reason": "max_output_tokens"}
	case finishReason(s.finish, false) == "content_filter":
		return "incomplete", map[string]any{"reason": "content_filter"}
	}
	return "completed", nil
}

// response 构造响应对象。final 为假时（response.created、response.in_progress）输出为空、没有用量。
func (s *ResponsesStreamer) response(status string, incomplete, failure map[string]any, final bool) map[string]any {
	r := map[string]any{
		"id": s.id, "object": "response", "created_at": s.created, "status": status, "background": false,
		"error": failure, "incomplete_details": incomplete, "model": s.req.Model, "output": []any{},
		"previous_response_id": nil, "store": false, "usage": nil,
	}
	for k, v := range s.req.Echo {
		r[k] = v
	}
	if final {
		if len(s.output) > 0 {
			r["output"] = s.output
		}
		r["usage"] = responsesUsage(s.usage)
	}
	return r
}

// responsesUsage 是 Responses 的用量：input_tokens 含命中缓存的部分，output_tokens 含思考。
func responsesUsage(u *geminiUsage) map[string]any {
	c := usageFrom(u)
	return map[string]any{
		"input_tokens":          c.PromptTokens,
		"input_tokens_details":  map[string]any{"cached_tokens": c.PromptTokensDetails.CachedTokens},
		"output_tokens":         c.CompletionTokens,
		"output_tokens_details": map[string]any{"reasoning_tokens": c.CompletionTokensDetails.ReasoningTokens},
		"total_tokens":          c.TotalTokens,
	}
}

// start 发 response.created 和 response.in_progress。
func (s *ResponsesStreamer) start() {
	if s.started {
		return
	}
	s.started = true
	r := s.response("in_progress", nil, nil, false)
	s.emit("response.created", map[string]any{"response": r})
	s.emit("response.in_progress", map[string]any{"response": r})
}

// open 开一个新的 reasoning 或 message 条目（先关掉正在输出的）。
func (s *ResponsesStreamer) open(kind string) *outputItem {
	s.closeItem()
	it := &outputItem{kind: kind, index: len(s.output)}
	var item map[string]any
	if kind == "reasoning" {
		it.id = "rs_" + randomString(32)
		item = map[string]any{"id": it.id, "type": "reasoning", "summary": []any{}}
	} else {
		it.id = "msg_" + randomString(32)
		item = map[string]any{"id": it.id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}
	}
	s.cur = it
	s.emit("response.output_item.added", map[string]any{"output_index": it.index, "item": item})
	return it
}

func (s *ResponsesStreamer) reasoningDelta(text string) {
	it := s.cur
	if it == nil || it.kind != "reasoning" {
		it = s.open("reasoning")
	}
	if !it.partOpen {
		it.partOpen = true
		s.emit("response.reasoning_summary_part.added", map[string]any{"item_id": it.id, "output_index": it.index,
			"summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
	}
	it.text.WriteString(text)
	s.emit("response.reasoning_summary_text.delta", map[string]any{"item_id": it.id, "output_index": it.index,
		"summary_index": 0, "delta": text})
}

func (s *ResponsesStreamer) messageDelta(text string) {
	it := s.cur
	if it == nil || it.kind != "message" {
		it = s.open("message")
	}
	if !it.partOpen {
		it.partOpen = true
		s.emit("response.content_part.added", map[string]any{"item_id": it.id, "output_index": it.index,
			"content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
	}
	it.text.WriteString(text)
	s.emit("response.output_text.delta", map[string]any{"item_id": it.id, "output_index": it.index,
		"content_index": 0, "delta": text, "logprobs": []any{}})
}

// closeItem 结束正在输出的条目：发各自的 done 事件和 output_item.done，条目进 output。
func (s *ResponsesStreamer) closeItem() {
	it := s.cur
	if it == nil {
		return
	}
	s.cur = nil
	text := it.text.String()
	var item map[string]any
	if it.kind == "reasoning" {
		summary := []any{}
		if it.partOpen {
			part := map[string]any{"type": "summary_text", "text": text}
			s.emit("response.reasoning_summary_text.done", map[string]any{"item_id": it.id, "output_index": it.index,
				"summary_index": 0, "text": text})
			s.emit("response.reasoning_summary_part.done", map[string]any{"item_id": it.id, "output_index": it.index,
				"summary_index": 0, "part": part})
			summary = append(summary, part)
		}
		item = map[string]any{"id": it.id, "type": "reasoning", "summary": summary}
		if s.req.EncryptedReasoning && it.sig != "" {
			item["encrypted_content"] = it.sig
		}
	} else {
		part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}
		s.emit("response.output_text.done", map[string]any{"item_id": it.id, "output_index": it.index,
			"content_index": 0, "text": text, "logprobs": []any{}})
		s.emit("response.content_part.done", map[string]any{"item_id": it.id, "output_index": it.index,
			"content_index": 0, "part": part})
		item = map[string]any{"id": it.id, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}
	}
	s.done(it.index, item)
}

// toolCall 输出一个工具调用：客户端声明的自定义工具是 custom_tool_call（取 args.input），其余是 function_call；
// 命名空间工具的名字拆回命名空间和函数名。
func (s *ResponsesStreamer) toolCall(p geminiPart) {
	args := compactArgs(p.FunctionCall.Args)
	index := len(s.output)
	ref, known := s.req.Tools[p.FunctionCall.Name]
	name := p.FunctionCall.Name
	if known {
		name = ref.Name
	}
	item := map[string]any{"call_id": toolID(p.FunctionCall.ID), "name": name, "status": "in_progress"}
	if ref.Namespace != "" {
		item["namespace"] = ref.Namespace
	}
	if ref.Custom {
		input := customInput(args)
		item["id"], item["type"], item["input"] = "ctc_"+randomString(32), "custom_tool_call", ""
		s.emit("response.output_item.added", map[string]any{"output_index": index, "item": item})
		s.emit("response.custom_tool_call_input.delta", map[string]any{"item_id": item["id"], "output_index": index, "delta": input})
		s.emit("response.custom_tool_call_input.done", map[string]any{"item_id": item["id"], "output_index": index, "input": input})
		item["input"] = input
	} else {
		item["id"], item["type"], item["arguments"] = "fc_"+randomString(32), "function_call", ""
		s.emit("response.output_item.added", map[string]any{"output_index": index, "item": item})
		s.emit("response.function_call_arguments.delta", map[string]any{"item_id": item["id"], "output_index": index, "delta": args})
		s.emit("response.function_call_arguments.done", map[string]any{"item_id": item["id"], "output_index": index,
			"name": name, "arguments": args})
		item["arguments"] = args
	}
	item["status"] = "completed"
	s.done(index, item)
}

// customInput 取自定义工具调用的输入文本（args.input）；模型没按约定给时原样用参数 JSON。
func customInput(args string) string {
	var v struct {
		Input *string `json:"input"`
	}
	if json.Unmarshal([]byte(args), &v) == nil && v.Input != nil {
		return *v.Input
	}
	return args
}

// done 发 output_item.done，条目进 output。
func (s *ResponsesStreamer) done(index int, item map[string]any) {
	s.emit("response.output_item.done", map[string]any{"output_index": index, "item": item})
	s.output = append(s.output, item)
}

// emit 发一个事件（带 type 和递增的 sequence_number）；非流式时不发。
func (s *ResponsesStreamer) emit(typ string, data map[string]any) {
	if s.w == nil || s.err != nil {
		return
	}
	data["type"] = typ
	data["sequence_number"] = s.seq
	s.seq++
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", typ, raw); err != nil {
		s.err = err
		return
	}
	if s.flush != nil {
		s.flush()
	}
}
