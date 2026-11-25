package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shiceai/agy2api/internal/anthropic"
	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/relay"
)

// Anthropic Messages 入站：/v1/messages、/v1/messages/count_tokens，以及带 anthropic-version 头的 /v1/models。
// 请求转成 Gemini 格式交给执行层（选号、换号、会话粘性、用量记录等都复用），响应再转回 Messages 格式
// （转换见 internal/anthropic）。

// isAnthropicRequest 报告请求是不是走 Anthropic 接口：/v1/messages 下的路径，或带 anthropic-version 头
// （Anthropic SDK 每个请求都带；/v1/models 靠它区分两种格式）。
func isAnthropicRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/v1/messages") || r.Header.Get("anthropic-version") != ""
}

// readAnthropicRequest 读取并转换 Messages 请求体，失败时已写回 413 或 400。
func readAnthropicRequest(w http.ResponseWriter, r *http.Request, countOnly bool) (*anthropic.Request, bool) {
	body, err := readRequestBody(w, r, inboundBodyLimit)
	switch {
	case errors.Is(err, errBodyTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, anthropic.ErrorBody(http.StatusRequestEntityTooLarge, "request body exceeds 16 MiB"))
		return nil, false
	case err != nil:
		writeJSON(w, http.StatusBadRequest, anthropic.ErrorBody(http.StatusBadRequest, "read body: "+err.Error()))
		return nil, false
	}
	req, err := anthropic.ParseRequest(body, countOnly)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, anthropic.ErrorBody(http.StatusBadRequest, err.Error()))
		return nil, false
	}
	if len(req.Dropped) > 0 {
		// Claude Code 每个请求都带 web_search 等服务端工具，记 debug，不刷屏。
		slog.DebugContext(r.Context(), "Messages 请求里有上游不支持的内容，已去掉", "dropped", req.Dropped)
	}
	return req, true
}

// handleMessages 处理 POST /v1/messages。
func (s *Service) handleMessages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	in, ok := readAnthropicRequest(w, r, false)
	if !ok {
		return
	}
	model := s.resolveModel(in.Model)
	req := inboundRequest(r, model, in.Model, in.Stream)
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
			writeJSON(w, http.StatusBadGateway, anthropic.ErrorBody(http.StatusBadGateway, "read upstream response: "+err.Error()))
			return
		}
		out, err := anthropic.ConvertResponse(raw, in.Model)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, anthropic.ErrorBody(http.StatusBadGateway, "invalid upstream response"))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
		return
	}

	startSSE(w)
	st := anthropic.NewStreamer(w, func() { flush(w) }, in.Model)
	// 执行层的流式响应每块是一行 "data: {...}"（见 antigravity.sseUnwrapper），逐行转成 Messages 事件。
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if data, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r\n"), []byte("data: ")); ok {
			st.Chunk(data)
		}
		if st.Err() != nil {
			return // 客户端断开了
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			st.Finish()
		} else if re := relay.StreamFailure(err); re != nil {
			st.Error(re.Status, re.Message)
		}
		return
	}
}

// handleMessagesCountTokens 处理 POST /v1/messages/count_tokens：优先问上游，失败时本地估算（同 Gemini 的 countTokens）。
func (s *Service) handleMessagesCountTokens(w http.ResponseWriter, r *http.Request) {
	in, ok := readAnthropicRequest(w, r, true)
	if !ok {
		return
	}
	model := s.resolveModel(in.Model)
	if !keyAllowsModel(apiKeyFromContext(r.Context()), model) {
		modelNotAllowed(w, r, model)
		return
	}
	out, err := s.relay.CountTokens(r.Context(), model, in.Body)
	if errors.Is(err, relay.ErrModelNotFound) {
		writeJSON(w, http.StatusNotFound, anthropic.ErrorBody(http.StatusNotFound, "model not found: "+in.Model))
		return
	}
	var counted struct {
		TotalTokens int64 `json:"totalTokens"`
	}
	if err != nil || json.Unmarshal(out, &counted) != nil {
		slog.WarnContext(r.Context(), "count_tokens 上游不可用，改用本地估算", "model", model, "err", err)
		counted.TotalTokens = int64(antigravity.LocalTokenEstimate(in.Body))
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": counted.TotalTokens})
}
