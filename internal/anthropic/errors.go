package anthropic

import "net/http"

// ErrorType 按状态码给出 Messages 的错误类型。
func ErrorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable, 529:
		return "overloaded_error"
	default:
		if status >= 400 && status < 500 {
			return "invalid_request_error"
		}
		return "api_error"
	}
}

// ErrorBody 是 Messages 的错误响应体（非流式的响应体、流式的 error 事件内容都是它）。
func ErrorBody(status int, message string) map[string]any {
	return map[string]any{"type": "error", "error": map[string]any{"type": ErrorType(status), "message": message}}
}

// ModelRecord 是 GET /v1/models（Anthropic 格式）里的一条。created_at 上游没有，填固定值。
func ModelRecord(id, displayName string) map[string]any {
	if displayName == "" {
		displayName = id
	}
	return map[string]any{"type": "model", "id": id, "display_name": displayName, "created_at": "2026-01-01T00:00:00Z"}
}

// ModelList 是 Anthropic 格式的模型列表（一次全部返回，不分页）。
func ModelList(records []map[string]any) map[string]any {
	out := map[string]any{"data": records, "has_more": false, "first_id": nil, "last_id": nil}
	if n := len(records); n > 0 {
		out["first_id"] = records[0]["id"]
		out["last_id"] = records[n-1]["id"]
	}
	return out
}
