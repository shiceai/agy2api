package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Streamer 把 Gemini 流式响应的每一块转成 chat.completion.chunk：第一块带 role，之后是 content、reasoning_content
// 和 tool_calls 的增量，最后一块带 finish_reason；客户端要了用量（stream_options.include_usage）时再发一块
// choices 为空的用量；最后是 data: [DONE]。
type Streamer struct {
	w            io.Writer
	flush        func()
	model        string
	includeUsage bool
	id           string
	created      int64

	started  bool
	finished bool
	tools    int // 已发的工具调用数，也是下一个工具调用的 index
	finish   string
	usage    *geminiUsage
	// err 是写客户端失败的错误，之后不再写。
	err error
}

// NewStreamer 创建转换器；flush 在每块后调用（把数据推给客户端），可为 nil。
func NewStreamer(w io.Writer, flush func(), model string, includeUsage bool) *Streamer {
	return &Streamer{w: w, flush: flush, model: model, includeUsage: includeUsage,
		id: NewCompletionID(), created: time.Now().Unix()}
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
			if p.Text != "" {
				s.delta(map[string]any{"reasoning_content": p.Text}, nil)
			}
		case p.FunctionCall != nil:
			call := toolCall(p)
			call["index"] = s.tools
			s.tools++
			s.delta(map[string]any{"tool_calls": []any{call}}, nil)
		case p.Text != "":
			s.delta(map[string]any{"content": p.Text}, nil)
		}
	}
	if cand.FinishReason != "" {
		s.finish = cand.FinishReason
	}
}

// Finish 结束流：发带 finish_reason 的最后一块、用量（客户端要了的话）和 [DONE]。
func (s *Streamer) Finish() {
	if s.finished {
		return
	}
	s.start()
	s.finished = true
	s.delta(map[string]any{}, finishReason(s.finish, s.tools > 0))
	if s.includeUsage {
		s.write(s.chunk([]any{}, usageFrom(s.usage)))
	}
	s.raw("[DONE]")
}

// Error 在流里发一个错误块（流已经开始，状态码改不了了），之后不再输出。
func (s *Streamer) Error(status int, message string) {
	if s.finished {
		return
	}
	s.finished = true
	s.write(ErrorBody(status, message))
}

// start 发第一块：只有 role 和空 content。
func (s *Streamer) start() {
	if s.started {
		return
	}
	s.started = true
	s.delta(map[string]any{"role": "assistant", "content": ""}, nil)
}

// delta 发一块只有一个 choice 的增量；finish 是 finish_reason（字符串，或 nil 表示还没结束）。
func (s *Streamer) delta(d map[string]any, finish any) {
	s.write(s.chunk([]any{map[string]any{"index": 0, "delta": d, "logprobs": nil, "finish_reason": finish}}, nil))
}

// chunk 构造 chat.completion.chunk。客户端要了用量时，按 OpenAI 的写法每块都带 usage（只有最后一块非 null）。
func (s *Streamer) chunk(choices []any, usage any) map[string]any {
	c := map[string]any{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": choices}
	if s.includeUsage {
		c["usage"] = usage
	}
	return c
}

func (s *Streamer) write(data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	s.raw(string(raw))
}

func (s *Streamer) raw(data string) {
	if s.err != nil {
		return
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		s.err = err
		return
	}
	if s.flush != nil {
		s.flush()
	}
}
