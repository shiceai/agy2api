package server

import (
	"net/http"
	"strings"

	"github.com/shiceai/agy2api/internal/store"
)

// clientSession 从请求头取客户端给的会话标识，会话粘性优先按它认对话（见 relay.Request.Session）。只认有依据的写法：
//   - Claude Code 每个请求都带 X-Claude-Code-Session-Id（会话的 UUID），子代理另带 X-Claude-Code-Agent-Id
//     （2.1.332 实抓）。子代理单独认对话：并行的子代理挤满并发换号时，不会把主会话记下的账号一起改掉。
//   - X-Session-Id：通用写法，多人共用一个 Key、前面还有一层网关时，可以按人或按会话设置。
//
// 都没有时返回空，由入站协议补（OpenAI 的 prompt_cache_key），再没有就由执行层按首条用户消息认。
func clientSession(r *http.Request) (id, source string) {
	if s := strings.TrimSpace(r.Header.Get("X-Claude-Code-Session-Id")); s != "" {
		if agent := strings.TrimSpace(r.Header.Get("X-Claude-Code-Agent-Id")); agent != "" {
			return s + "/" + agent, store.SessionClaudeCodeAgent
		}
		return s, store.SessionClaudeCode
	}
	if s := strings.TrimSpace(r.Header.Get("X-Session-Id")); s != "" {
		return s, store.SessionHeader
	}
	return "", ""
}
