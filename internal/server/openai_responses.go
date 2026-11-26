package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/openai"
	"github.com/shiceai/agy2api/internal/relay"
	"github.com/shiceai/agy2api/internal/store"
)

// OpenAI Responses 入站：POST /v1/responses（Codex 等）。只做无状态的部分，不存响应（见 internal/openai 的
// ParseResponsesRequest）。请求转成 Gemini 格式交给执行层，响应再转回 Responses 格式；错误格式同 Chat Completions。

// readResponsesRequest 读取并转换 Responses 请求体，失败时已写回 413 或 400。
// 开启客户端兼容（默认）时把客户端 system 降为开头的用户消息：上游会拦 Codex 的系统提示（见 anthropic_compat.go）。
func (s *Service) readResponsesRequest(w http.ResponseWriter, r *http.Request) (*openai.ResponsesRequest, bool) {
	body, err := readRequestBody(w, r, inboundBodyLimit)
	switch {
	case errors.Is(err, errBodyTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, openai.ErrorBody(http.StatusRequestEntityTooLarge, "request body exceeds 16 MiB"))
		return nil, false
	case err != nil:
		writeJSON(w, http.StatusBadRequest, openai.ErrorBody(http.StatusBadRequest, "read body: "+err.Error()))
		return nil, false
	}
	req, err := openai.ParseResponsesRequest(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, openai.ErrorBody(http.StatusBadRequest, err.Error()))
		return nil, false
	}
	if len(req.Dropped) > 0 {
		// Codex 每个请求都带 web_search 等内置工具，记 debug，不刷屏。
		slog.DebugContext(r.Context(), "Responses 请求里有上游不支持的内容，已去掉", "dropped", req.Dropped)
	}
	if s.clientSystemAsUser.Load() {
		antigravity.MoveSystemToUser(req.Body)
	}
	return req, true
}

// handleResponses 处理 POST /v1/responses。
func (s *Service) handleResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	in, ok := s.readResponsesRequest(w, r)
	if !ok {
		return
	}
	model := s.resolveModel(in.Model)
	req := inboundRequest(r, model, in.Model, in.Stream)
	if req.Session == "" && in.PromptCacheKey != "" {
		// Codex 把会话 ID 填在 prompt_cache_key 里。会话头优先。
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
		out, err := openai.ConvertResponsesResponse(raw, in)
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
	convertStream(resp.Body, openai.NewResponsesStreamer(w, func() { flush(w) }, in))
}
