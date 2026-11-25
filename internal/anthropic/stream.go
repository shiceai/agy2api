package anthropic

import (
	"encoding/json"
	"fmt"
	"io"
)

// Streamer 把 Gemini 流式响应的每一块转成 Messages 的流式事件：message_start，每个内容块的
// content_block_start、delta、content_block_stop，最后 message_delta（stop_reason、用量）和 message_stop。
//
// message_start 等第一块到了再发，好带上输入 Token 数（Claude Code 用它显示上下文占用）。
type Streamer struct {
	w     io.Writer
	flush func()
	model string

	started  bool
	finished bool
	next     int    // 下一个内容块的序号
	open     string // 正在输出的块类型：text、thinking，空表示没有
	toolUse  bool
	finish   string
	usage    *geminiUsage
	// err 是写客户端失败的错误，之后不再写。
	err error
}

// NewStreamer 创建转换器；flush 在每个事件后调用（把数据推给客户端），可为 nil。
func NewStreamer(w io.Writer, flush func(), model string) *Streamer {
	return &Streamer{w: w, flush: flush, model: model}
}

// Err 返回写客户端时的错误（客户端断开等）。
func (s *Streamer) Err() error { return s.err }

// Chunk 处理一块 Gemini 流式响应（SSE data 的内容）。
func (s *Streamer) Chunk(raw []byte) {
	var g geminiResponse
	if err := json.Unmarshal(raw, &g); err != nil {
		return
	}
	if g.UsageMetadata != nil {
		s.usage = g.UsageMetadata
	}
	s.start()
	if len(g.Candidates) == 0 {
		if g.PromptFeedback != nil && g.PromptFeedback.BlockReason != "" {
			s.finish = "SAFETY"
		}
		return
	}
	cand := g.Candidates[0]
	for _, p := range cand.Content.Parts {
		switch {
		case p.Thought:
			if p.Text == "" && (p.ThoughtSignature == "" || p.ThoughtSignature == skipThoughtSignature) {
				continue // 空的思考 part（Gemini 流里偶尔有），不开空块
			}
			s.ensure("thinking")
			if p.Text != "" {
				s.delta(map[string]any{"type": "thinking_delta", "thinking": p.Text})
			}
			if sig := p.ThoughtSignature; sig != "" && sig != skipThoughtSignature {
				s.delta(map[string]any{"type": "signature_delta", "signature": sig})
				s.closeBlock() // 签名在思考的最后一块上
			}
		case p.FunctionCall != nil:
			s.closeBlock()
			if sig := p.ThoughtSignature; sig != "" && sig != skipThoughtSignature {
				// Gemini 的签名挂在工具调用上：插一个只带签名的思考块，客户端下一轮传回时挂回去。
				s.ensure("thinking")
				s.delta(map[string]any{"type": "signature_delta", "signature": sig})
				s.closeBlock()
			}
			s.toolUse = true
			idx := s.next
			s.next++
			s.event("content_block_start", map[string]any{"type": "content_block_start", "index": idx,
				"content_block": map[string]any{"type": "tool_use", "id": toolID(p.FunctionCall.ID), "name": p.FunctionCall.Name, "input": map[string]any{}}})
			s.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": string(argsOrEmpty(p.FunctionCall.Args))}})
			s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		case p.Text != "":
			s.ensure("text")
			s.delta(map[string]any{"type": "text_delta", "text": p.Text})
		}
	}
	if cand.FinishReason != "" {
		s.finish = cand.FinishReason
	}
}

// Finish 结束流：关掉正在输出的块，发 message_delta 和 message_stop。
func (s *Streamer) Finish() {
	if s.finished {
		return
	}
	s.start()
	s.closeBlock()
	s.finished = true
	u := usageFrom(s.usage)
	s.event("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": stopReason(s.finish, s.toolUse), "stop_sequence": nil},
		"usage": map[string]any{"input_tokens": u.InputTokens, "cache_read_input_tokens": u.CacheReadInputTokens,
			"cache_creation_input_tokens": 0, "output_tokens": u.OutputTokens}})
	s.event("message_stop", map[string]any{"type": "message_stop"})
}

// Error 在流里发一个错误事件（流已经开始，状态码改不了了），之后不再输出。
func (s *Streamer) Error(status int, message string) {
	if s.finished {
		return
	}
	s.finished = true
	s.event("error", ErrorBody(status, message))
}

func (s *Streamer) start() {
	if s.started {
		return
	}
	s.started = true
	u := usageFrom(s.usage)
	s.event("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": NewMessageID(), "type": "message", "role": "assistant", "model": s.model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": u.InputTokens, "cache_read_input_tokens": u.CacheReadInputTokens,
			"cache_creation_input_tokens": 0, "output_tokens": 0},
	}})
}

// ensure 保证正在输出的是 kind 类型的块，不是就关掉当前块、开一个新块。
func (s *Streamer) ensure(kind string) {
	if s.open == kind {
		return
	}
	s.closeBlock()
	block := map[string]any{"type": kind, "text": ""}
	if kind == "thinking" {
		block = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	}
	s.event("content_block_start", map[string]any{"type": "content_block_start", "index": s.next, "content_block": block})
	s.open = kind
}

func (s *Streamer) delta(d map[string]any) {
	s.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": s.next, "delta": d})
}

func (s *Streamer) closeBlock() {
	if s.open == "" {
		return
	}
	s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.next})
	s.open = ""
	s.next++
}

func (s *Streamer) event(name string, data any) {
	if s.err != nil {
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, raw); err != nil {
		s.err = err
		return
	}
	if s.flush != nil {
		s.flush()
	}
}
