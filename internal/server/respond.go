package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// geminiError 以 Gemini/Google 风格返回错误体。
func geminiError(w http.ResponseWriter, status int, statusText, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    status,
			"message": message,
			"status":  statusText,
		},
	})
}

// adminError 以简单结构返回管理端错误。
func adminError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

// errBodyTooLarge 表示请求体超过上限。
var errBodyTooLarge = errors.New("request body too large")

// readRequestBody 读取请求体，超过 limit 字节时返回 errBodyTooLarge，不静默截断。
func readRequestBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, errBodyTooLarge
	}
	return body, err
}

// decodeAdminJSON 读取并解析管理端请求体，失败时已写回 413 或 400。空请求体按 {} 处理。
func decodeAdminJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	raw, err := readRequestBody(w, r, limit)
	switch {
	case errors.Is(err, errBodyTooLarge):
		adminError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds %d bytes", limit))
		return false
	case err != nil:
		adminError(w, http.StatusBadRequest, "read body: "+err.Error())
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, v); err != nil {
		adminError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}
