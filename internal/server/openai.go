package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shiceai/agy2api/internal/openai"
	"github.com/shiceai/agy2api/internal/relay"
	"github.com/shiceai/agy2api/internal/store"
)

// OpenAI Chat Completions 入站：/v1/chat/completions，以及不带 Gemini、Anthropic 区分头的 GET /v1/models。
// 请求转成 Gemini 格式交给执行层（选号、换号、会话粘性、用量记录等都复用），响应再转回 OpenAI 格式
// （转换见 internal/openai）。

// isOpenAIModelsRequest 报告是不是 OpenAI 客户端查模型：GET /v1/models 或 /v1/models/{id}，且不带 Gemini 客户端的
// x-goog-api-key 或 ?key=（Gemini SDK 都带）。Anthropic 的 anthropic-version 头在调用方先判断。
func isOpenAIModelsRequest(r *http.Request) bool {
	if r.Method != http.MethodGet || r.Header.Get("x-goog-api-key") != "" || r.URL.Query().Get("key") != "" {
		return false
	}
	p := r.URL.Path
	return p == "/v1/models" || (strings.HasPrefix(p, "/v1/models/") && !strings.Contains(p, ":"))
}

// readOpenAIRequest 读取并转换 Chat Completions 请求体，失败时已写回 413 或 400。
func readOpenAIRequest(w http.ResponseWriter, r *http.Request) (*openai.Request, bool) {
	body, err := readRequestBody(w, r, inboundBodyLimit)
	switch {
	case errors.Is(err, errBodyTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, openai.ErrorBody(http.StatusRequestEntityTooLarge, "request body exceeds 16 MiB"))
		return nil, false
	case err != nil:
		writeJSON(w, http.StatusBadRequest, openai.ErrorBody(http.StatusBadRequest, "read body: "+err.Error()))
		return nil, false
	}
	req, err := openai.ParseRequest(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, openai.ErrorBody(http.StatusBadRequest, err.Error()))
		return nil, false
	}
	if len(req.Dropped) > 0 {
		slog.DebugContext(r.Context(), "Chat Completions 请求里有上游不支持的内容，已去掉", "dropped", req.Dropped)
	}
	return req, true
}

// handleChatCompletions 处理 POST /v1/chat/completions。
func (s *Service) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	in, ok := readOpenAIRequest(w, r)
	if !ok {
		return
	}
	model := s.resolveModel(in.Model)
	req := inboundRequest(r, model, in.Model, in.Stream)
	if req.Session == "" && in.PromptCacheKey != "" {
		// OpenAI 官方的缓存路由字段：同一个 key 的请求共享缓存，正好用来认对话。会话头优先。
		req.Session, req.SessionSource = in.PromptCacheKey, store.SessionPromptCacheKey
	}
	if !keyAllowsModel(apiKeyFromContext(r.Context()), model) {
		s.relay.RecordRejected(r.Context(), req, start, http.StatusForbidden,
			relay.KindModelNotAllowed, "model "+model+" is not allowed for this API key")
		modelNotAllowed(w, r, model)
		return
	}
	if s.quotaExceeded(w, r, req, start) {
		return
	}
	req.Body = in.Body
	resp, err := s.relay.Generate(r.Context(), req)
	if err != nil {
		writeRelayError(w, r, err, model, in.Stream)
		return
	}
	defer resp.Close()

	if !in.Stream {
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, openai.ErrorBody(http.StatusBadGateway, "read upstream response: "+err.Error()))
			return
		}
		out, err := openai.ConvertResponse(raw, in.Model)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, openai.ErrorBody(http.StatusBadGateway, "invalid upstream response"))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
		return
	}

	startSSE(w)
	convertStream(resp.Body, openai.NewStreamer(w, func() { flush(w) }, in.Model, in.IncludeUsage))
}
