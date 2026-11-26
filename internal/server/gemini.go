package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shiceai/agy2api/internal/anthropic"
	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/openai"
	"github.com/shiceai/agy2api/internal/relay"
	"github.com/shiceai/agy2api/internal/store"
)

// inboundBodyLimit 是入站 Gemini 请求体上限（16 MiB）。
const inboundBodyLimit = 16 << 20

// listModels 返回启用账号目录的并集；Key 限制了模型时只列允许的。格式按调用的接口（见 inboundProtocol）：
// Gemini 的 models[]，Anthropic 或 OpenAI 的 data[]。
func (s *Service) listModels(w http.ResponseWriter, r *http.Request) {
	key := apiKeyFromContext(r.Context())
	catalog := s.relay.Models()
	models := make([]map[string]any, 0, len(catalog))
	protocol := inboundProtocol(r)
	for _, m := range catalog {
		if !keyAllowsModel(key, m.ID) {
			continue
		}
		switch protocol {
		case store.ProtocolAnthropic:
			models = append(models, anthropic.ModelRecord(m.ID, m.DisplayName))
		case store.ProtocolOpenAI:
			models = append(models, openai.ModelRecord(m.ID))
		default:
			models = append(models, geminiModelRecord(m.ID, m))
		}
	}
	switch protocol {
	case store.ProtocolAnthropic:
		writeJSON(w, http.StatusOK, anthropic.ModelList(models))
	case store.ProtocolOpenAI:
		writeJSON(w, http.StatusOK, openai.ModelList(models))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
	}
}

// geminiModelRecord 构造 Gemini 模型记录。id 是对外的模型名，家族名请求时与 m.ID 不同。
func geminiModelRecord(id string, m antigravity.ModelInfo) map[string]any {
	name := m.DisplayName
	if name == "" {
		name = m.ID
	}
	return map[string]any{
		"name":                       "models/" + id,
		"displayName":                name,
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
	}
}

// getModel 返回单个模型记录，家族名按默认档位解析；带假流式前缀的按去掉前缀的模型查。
func (s *Service) getModel(w http.ResponseWriter, r *http.Request) {
	model := strings.TrimPrefix(modelPath(r), "models/")
	base, _ := stripFakeStream(model)
	base = s.resolveModel(base)
	if !keyAllowsModel(apiKeyFromContext(r.Context()), base) {
		modelNotAllowed(w, r, model)
		return
	}
	if m, ok := s.relay.LookupModel(r.Context(), base); ok {
		switch inboundProtocol(r) {
		case store.ProtocolAnthropic:
			writeJSON(w, http.StatusOK, anthropic.ModelRecord(model, m.DisplayName))
		case store.ProtocolOpenAI:
			writeJSON(w, http.StatusOK, openai.ModelRecord(model))
		default:
			writeJSON(w, http.StatusOK, geminiModelRecord(model, m))
		}
		return
	}
	inboundError(w, r, http.StatusNotFound, "NOT_FOUND", "model not found: "+model)
}

// handleModelAction 解析 {model}:{method} 并分派到生成/计数。
func (s *Service) handleModelAction(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	modelAction := modelPath(r)
	model, method, ok := splitModelAction(modelAction)
	if !ok {
		geminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"expected models/{model}:{method}")
		return
	}
	// 假流式前缀：流式请求走 handleFakeStream，其余方法只去掉前缀。允许的模型按去掉前缀的名字判断。
	model, fake := stripFakeStream(model)
	// 模型别名：之后都用解析出的模型名（含 Key 允许模型的判断），用量里记客户端原来的名字。
	requested := model
	model = s.resolveModel(model)
	stream := method == "streamGenerateContent"
	if !keyAllowsModel(apiKeyFromContext(r.Context()), model) {
		// 生成请求被拒也记一条用量（计数请求不记）。
		if stream || method == "generateContent" {
			req := inboundRequest(r, model, requested, stream && !fake)
			req.FakeStream = stream && fake
			s.relay.RecordRejected(r.Context(), req, start, http.StatusForbidden,
				relay.KindModelNotAllowed, "model "+model+" is not allowed for this API key")
		}
		modelNotAllowed(w, r, model)
		return
	}
	switch method {
	case "streamGenerateContent":
		if fake {
			s.handleFakeStream(w, r, model, requested, start)
			return
		}
		s.handleGenerate(w, r, model, requested, true, start)
	case "generateContent":
		s.handleGenerate(w, r, model, requested, false, start)
	case "countTokens":
		s.handleCountTokens(w, r, model)
	default:
		geminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "unsupported method: "+method)
	}
}

// modelNotAllowed 返回 Key 不允许使用该模型的 403。
func modelNotAllowed(w http.ResponseWriter, r *http.Request, model string) {
	inboundError(w, r, http.StatusForbidden, "PERMISSION_DENIED", "model "+model+" is not allowed for this API key")
}

// inboundProtocol 判断请求走的入站接口（store.ProtocolGemini 等）：
//   - /v1/chat/ 下的路径是 OpenAI；
//   - /v1/messages 下的路径，或带 anthropic-version 头（Anthropic SDK 每个请求都带）是 Anthropic；
//   - 不带 Gemini 客户端 Key 头的 GET /v1/models（见 isOpenAIModelsRequest）是 OpenAI；
//   - 其余是 Gemini。
func inboundProtocol(r *http.Request) string {
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/chat/"):
		return store.ProtocolOpenAI
	case strings.HasPrefix(r.URL.Path, "/v1/messages"), r.Header.Get("anthropic-version") != "":
		return store.ProtocolAnthropic
	case isOpenAIModelsRequest(r):
		return store.ProtocolOpenAI
	}
	return store.ProtocolGemini
}

// inboundError 按入站接口的格式写错误：Anthropic、OpenAI 接口用各自的错误格式，其余用 Gemini 的。
func inboundError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	switch inboundProtocol(r) {
	case store.ProtocolAnthropic:
		writeJSON(w, status, anthropic.ErrorBody(status, msg))
	case store.ProtocolOpenAI:
		writeJSON(w, status, openai.ErrorBody(status, msg))
	default:
		geminiError(w, status, code, msg)
	}
}

// quotaExceeded 检查 Key 本周期的 Token 是否已用完，用完时写回错误：每天、每月的周期返回 429 并带 Retry-After
// （到下个周期开始），总量用完返回 403（重试也没用，避免客户端反复重试）。被拒的请求记一条用量。
func (s *Service) quotaExceeded(w http.ResponseWriter, r *http.Request, req relay.Request, start time.Time) bool {
	key := apiKeyFromContext(r.Context())
	if key == nil {
		return false
	}
	now := time.Now()
	exceeded, resetAt := s.keys.QuotaExceeded(key, now)
	if !exceeded {
		return false
	}
	status, code, msg := http.StatusForbidden, "PERMISSION_DENIED", "API key token quota exhausted"
	if !resetAt.IsZero() {
		status, code = http.StatusTooManyRequests, "RESOURCE_EXHAUSTED"
		msg = "API key token quota exceeded for this " + key.TokenPeriod + ", resets at " + resetAt.Format(time.RFC3339)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(resetAt.Sub(now).Seconds()))))
	}
	s.relay.RecordRejected(r.Context(), req, start, status, relay.KindKeyQuota, msg)
	inboundError(w, r, status, code, msg)
	return true
}

// inboundRequest 用入站请求的模型、调用方和 Key 构造执行层请求（请求体另填）。model 是别名解析后的模型名，
// requested 是客户端原来的模型名（记进用量）。接口按请求判断（见 inboundProtocol）。
func inboundRequest(r *http.Request, model, requested string, stream bool) relay.Request {
	req := relay.Request{Model: model, RequestModel: requested, Stream: stream, ClientIP: clientIP(r), UserAgent: r.UserAgent(),
		Protocol: inboundProtocol(r)}
	if key := apiKeyFromContext(r.Context()); key != nil {
		req.APIKeyID = key.ID
	}
	return req
}

// modelPath 返回路径里 /models/ 后面的部分（模型名和方法）。模型名里可能带 /（如 假流式/gemini-2.5-flash）；
// 客户端把 / 编码成 %2F 时 chi 按原始路径匹配，拿到的是编码后的字符串，这里解码回来。
func modelPath(r *http.Request) string {
	p := chi.URLParam(r, "*")
	if strings.Contains(p, "%") {
		if u, err := url.PathUnescape(p); err == nil {
			p = u
		}
	}
	return p
}

func splitModelAction(s string) (model, method string, ok bool) {
	s = strings.TrimPrefix(s, "models/")
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return "", "", false
	}
	return s[:idx], s[idx+1:], true
}

// readGeminiRequest 读取并解析入站 Gemini 请求体，失败时已写回 413 或 400。
func readGeminiRequest(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	body, err := readRequestBody(w, r, inboundBodyLimit)
	switch {
	case errors.Is(err, errBodyTooLarge):
		geminiError(w, http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "request body exceeds 16 MiB")
		return nil, false
	case err != nil:
		geminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "read body: "+err.Error())
		return nil, false
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		geminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid JSON body")
		return nil, false
	}
	return req, true
}

func (s *Service) handleGenerate(w http.ResponseWriter, r *http.Request, model, requested string, stream bool, start time.Time) {
	req := inboundRequest(r, model, requested, stream)
	if s.quotaExceeded(w, r, req, start) {
		return
	}
	geminiReq, ok := readGeminiRequest(w, r)
	if !ok {
		return
	}
	req.Body = geminiReq

	resp, err := s.relay.Generate(r.Context(), req)
	if err != nil {
		writeRelayError(w, r, err, model, stream)
		return
	}
	defer resp.Close()

	if stream {
		streamResponse(w, resp.Body)
		return
	}

	// 非流式结果已经整个在内存里（大小上限在 antigravity），原样转发，不再截断。
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

// clientIP 返回调用方 IP：realIP 中间件已按可信反代的转发头改写了 RemoteAddr（见 clientip.go）。
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// writeRelayError 把生成失败写成 Gemini 风格的错误响应：状态码、错误码、说明和 Retry-After 来自
// relay 的类型化错误，上游原始错误体不转发。503 这类服务端问题的说明后面带上请求 ID，方便对照日志排查。
func writeRelayError(w http.ResponseWriter, r *http.Request, err error, model string, stream bool) {
	var re *relay.Error
	if !errors.As(err, &re) {
		re = &relay.Error{Status: http.StatusServiceUnavailable, Code: "UNAVAILABLE", Message: "upstream is temporarily unavailable, retry later", Err: err}
	}
	// 内部原因只进日志。
	slog.WarnContext(r.Context(), "生成请求失败", "model", model, "stream", stream,
		"status", re.Status, "retry_after", re.RetryAfter, "err", re.Err)
	if re.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(re.RetryAfter.Seconds()))))
	}
	msg := re.Message
	if re.Status >= 500 {
		msg += " (request id: " + logx.RequestID(r.Context()) + ")"
	}
	inboundError(w, r, re.Status, re.Code, msg)
}

// startSSE 写 SSE 响应头和 200。
func startSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// streamResponse 把已解包的 SSE 逐块转发给客户端，每块立即 flush。
func streamResponse(w http.ResponseWriter, body io.Reader) {
	startSSE(w)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 16<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			// 上游中途出错（太久没数据被断开、推来错误帧）：状态码已经发出去了，补一个错误事件。
			if re := relay.StreamFailure(err); re != nil {
				writeSSEError(w, re.Status, re.Code, re.Message)
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}
	}
}

// writeSSEError 在已经开始的 SSE 流里写一个 Gemini 格式的错误事件。
func writeSSEError(w io.Writer, status int, statusText, message string) {
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"code": status, "message": message, "status": statusText}})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
}
