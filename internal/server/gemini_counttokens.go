package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/relay"
)

// handleCountTokens 处理 :countTokens。优先走上游；模型不存在返回 404，其它失败回退本地估算。
func (s *Service) handleCountTokens(w http.ResponseWriter, r *http.Request, model string) {
	geminiReq, ok := readGeminiRequest(w, r)
	if !ok {
		return
	}
	out, err := s.relay.CountTokens(r.Context(), model, geminiReq)
	if errors.Is(err, relay.ErrModelNotFound) {
		geminiError(w, http.StatusNotFound, "NOT_FOUND", "model not found: "+model)
		return
	}
	if err != nil {
		// 无账号或上游失败都回退本地估算。
		slog.WarnContext(r.Context(), "countTokens 上游不可用，改用本地估算", "model", model, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"totalTokens": antigravity.LocalTokenEstimate(geminiReq)})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
