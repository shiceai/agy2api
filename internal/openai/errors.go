package openai

import (
	"net/http"
	"strings"
)

// ErrorType 按状态码给出 OpenAI 的错误类型。
func ErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		if status >= 500 {
			return "server_error"
		}
		return "invalid_request_error"
	}
}

// ErrorBody 是 OpenAI 的错误响应体（非流式的响应体、流式里的错误块都是它）。
func ErrorBody(status int, message string) map[string]any {
	return map[string]any{"error": map[string]any{"message": message, "type": ErrorType(status), "param": nil, "code": nil}}
}

// modelCreated 是模型记录的 created：上游没有发布时间，填固定值（2026-01-01）。
const modelCreated = 1767225600

// ModelRecord 是 GET /v1/models（OpenAI 格式）里的一条，owned_by 按模型家族给。
func ModelRecord(id string) map[string]any {
	owner := "google"
	switch lower := strings.ToLower(id); {
	case strings.HasPrefix(lower, "claude"):
		owner = "anthropic"
	case strings.HasPrefix(lower, "gpt"):
		owner = "openai"
	}
	return map[string]any{"id": id, "object": "model", "created": modelCreated, "owned_by": owner}
}

// ModelList 是 OpenAI 格式的模型列表。
func ModelList(records []map[string]any) map[string]any {
	return map[string]any{"object": "list", "data": records}
}
