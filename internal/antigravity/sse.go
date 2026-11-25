package antigravity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
)

// sseUnwrapper 把上游 SSE（每条 data 为 {"response":{...}} 信封）逐条解包为标准 Gemini SSE。
// 同时跟踪最后一帧的用量，供流读完后读取。上游中途推来错误帧时不转发它，停止读取并返回 *StreamError。
type sseUnwrapper struct {
	source io.ReadCloser
	reader *bufio.Reader
	queue  bytes.Buffer
	// err 是流的结束原因（io.EOF、读取错误或 *StreamError），设上后不再读上游，队列读空后一直返回它。
	err error
	// lastRead 是最近一次从上游读到一行的时间（Unix 纳秒），含解包时丢掉的空事件和注释，空闲超时据此判断。
	lastRead atomic.Int64

	mu        sync.Mutex
	lastUsage *Usage
	// firstTokenAt 是第一个带内容（文本、思考或工具调用）的帧被读到的时间，用来算首字延迟。
	firstTokenAt time.Time
	// firstEventAt 是第一个上游帧（含不带内容的帧）被读到的时间。
	firstEventAt time.Time
	// firstTextAt 是第一个正文帧（非思考的文本或工具调用）被读到的时间。
	firstTextAt time.Time
	// responseID、traceID 取自第一个带它们的帧。
	responseID string
	traceID    string
	// hideThoughts 为真时解包后去掉思考 part（上游不认请求侧的 includeThoughts=false）。
	hideThoughts bool
}

func (s *sseUnwrapper) firstToken() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstTokenAt
}

// marks 返回流读取过程中记下的时间点和上游 ID。
func (s *sseUnwrapper) marks() (firstEvent, firstText time.Time, responseID, traceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstEventAt, s.firstTextAt, s.responseID, s.traceID
}

func newSSEUnwrapper(source io.ReadCloser, hideThoughts bool) *sseUnwrapper {
	s := &sseUnwrapper{source: source, reader: bufio.NewReader(source), hideThoughts: hideThoughts}
	s.lastRead.Store(time.Now().UnixNano())
	return s
}

func (s *sseUnwrapper) usage() *Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsage
}

func (s *sseUnwrapper) Close() error { return s.source.Close() }

// streamError 返回上游中途推来的错误帧，没有时为 nil。只在 Read 所在的协程调用（或读完之后）。
func (s *sseUnwrapper) streamError() *StreamError {
	se, _ := s.err.(*StreamError)
	return se
}

func (s *sseUnwrapper) Read(p []byte) (int, error) {
	for s.queue.Len() == 0 {
		if s.err != nil {
			return 0, s.err
		}
		s.fill()
	}
	return s.queue.Read(p)
}

// fill 读取下一条 SSE 事件，解包后写入输出队列；流结束、读取出错或遇到错误帧时设上 s.err。
func (s *sseUnwrapper) fill() {
	var data []string
	for {
		line, err := s.reader.ReadString('\n')
		if line != "" {
			s.lastRead.Store(time.Now().UnixNano())
		}
		if err != nil {
			if len(data) == 0 {
				s.err = err
				return
			}
			break // 流末尾没有空行时，用已累积的 data 收尾；下一次 fill 再读到同样的错误
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if len(data) == 0 {
				continue // 空事件，跳过
			}
			break
		}
		if strings.HasPrefix(trimmed, "data:") {
			data = append(data, strings.TrimSpace(trimmed[len("data:"):]))
			continue
		}
		// 其它字段（event/id/retry）忽略。
	}

	payload := strings.Join(data, "")
	if payload == "" || payload == "[DONE]" {
		return
	}
	// 错误帧：不转发上游原文（可能带内部细节），交给调用方分类处理、改写后告诉客户端。
	if ue, ok := inBandError([]byte(payload), nil, time.Now()); ok {
		s.err = &StreamError{ue}
		return
	}
	s.emit(unwrapStreamFrame([]byte(payload), s))
}

func (s *sseUnwrapper) emit(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	s.queue.WriteString("data: ")
	s.queue.Write(chunk)
	s.queue.WriteString("\n\n")
}

func (s *sseUnwrapper) recordUsage(u *Usage) {
	if u == nil {
		return
	}
	s.mu.Lock()
	// 流式用量帧是累计快照，保留最后一帧即可。
	s.lastUsage = u
	s.mu.Unlock()
}

// unwrapStreamFrame 解包单条流式信封 {"response":{...}} → {...}，并记录用量。
// 输出压缩成一行，保证 SSE 的 data 字段不被换行打断。
func unwrapStreamFrame(raw []byte, s *sseUnwrapper) []byte {
	inner, usage := unwrapGeminiJSON(raw)
	if usage != nil {
		s.recordUsage(usage)
	}
	now := time.Now()
	s.mu.Lock()
	if s.firstEventAt.IsZero() {
		s.firstEventAt = now
	}
	if s.firstTokenAt.IsZero() && gjson.GetBytes(inner, "candidates.0.content.parts.#").Int() > 0 {
		s.firstTokenAt = now
	}
	if s.firstTextAt.IsZero() && hasAnswerPart(inner) {
		s.firstTextAt = now
	}
	if s.responseID == "" {
		s.responseID = gjson.GetBytes(inner, "responseId").String()
	}
	if s.traceID == "" {
		s.traceID = gjson.GetBytes(raw, "traceId").String()
	}
	s.mu.Unlock()
	// 时间点和用量都用原始帧算好后，再去掉思考 part。
	if s.hideThoughts {
		inner = stripThoughtParts(inner)
	}
	var compact bytes.Buffer
	if json.Compact(&compact, inner) != nil {
		return inner
	}
	return compact.Bytes()
}

// hasAnswerPart 报告这一帧有没有正文：非思考的文本，或工具调用、代码等非文本内容。
func hasAnswerPart(inner []byte) bool {
	for _, p := range gjson.GetBytes(inner, "candidates.0.content.parts").Array() {
		if p.Get("thought").Bool() {
			continue
		}
		if p.Get("text").String() != "" || p.Get("functionCall").Exists() || p.Get("inlineData").Exists() || p.Get("executableCode").Exists() {
			return true
		}
	}
	return false
}
