package antigravity

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// conversationSalt 只用于派生会话和轨迹 ID，改了会让所有对话的 ID 一起变。
const conversationSalt = "agy2api:antigravity:conversation:v1\x00"

// conversation 是一次请求所属的对话，对应官方客户端的 cascade 和 trajectory。
//
// Gemini 协议的客户端每次都带完整历史、不带会话标识。同一账号、同一调用方、同一首条用户消息
// 视为同一对话：会话 ID、轨迹 ID 由此稳定派生，步数取历史里模型回复的条数。这样多轮对话的步数
// 像官方客户端一样随轮次增长，网关也不用保存会话状态。
type conversation struct {
	cascadeID    string
	trajectoryID string
	// step 是已完成的步数（新对话为 0），labels 里的步进编号用它；requestId 末段是 step+1。
	step int
}

func conversationFor(accountID int64, caller string, inner map[string]any) conversation {
	seed := fmt.Sprintf("%d\x00%s\x00%s", accountID, caller, FirstUserContent(inner))
	return conversation{
		cascadeID:    derivedUUID("cascade", seed),
		trajectoryID: derivedUUID("trajectory", seed),
		step:         countModelTurns(inner),
	}
}

// requestID 是信封的 requestId，官方格式 agent/<会话ID>/<毫秒>/<轨迹ID>/<步数>，步数从 1 开始。
func (c conversation) requestID(now time.Time) string {
	return fmt.Sprintf("agent/%s/%d/%s/%d", c.cascadeID, now.UnixMilli(), c.trajectoryID, c.step+1)
}

// labels 是 request.labels 的 9 个字段，对齐官方客户端实抓：步进编号从 0 开始，
// model_enum 填目录下发的枚举名（目录里没有时不填）。
func (c conversation) labels(wireModel string, model *ModelInfo) map[string]any {
	lower := strings.ToLower(wireModel)
	usedClaude := strconv.FormatBool(strings.Contains(lower, "claude"))
	labels := map[string]any{
		"cascade_id":               c.cascadeID,
		"root_cascade_id":          c.cascadeID,
		"trajectory_id":            c.trajectoryID,
		"request_id":               fmt.Sprintf("%s-%d", c.trajectoryID, c.step),
		"last_step_index":          strconv.Itoa(c.step),
		"used_claude":              usedClaude,
		"used_claude_conservative": usedClaude,
		"used_non_gemini_model":    strconv.FormatBool(!strings.HasPrefix(lower, "gemini")),
	}
	if model != nil && model.Enum != "" {
		labels["model_enum"] = model.Enum
	}
	return labels
}

// FirstUserContent 返回第一条用户消息的规范 JSON（map 键有序），没有时返回空串。对话的会话 ID 和会话粘性都按它认对话。
// 用整条消息而不只是文字，只有图片的对话也能区分开。
func FirstUserContent(inner map[string]any) string {
	contents, _ := inner["contents"].([]any)
	for _, c := range contents {
		content, ok := c.(map[string]any)
		if !ok || content["role"] != "user" {
			continue
		}
		raw, err := json.Marshal(content["parts"])
		if err != nil {
			return ""
		}
		return string(raw)
	}
	return ""
}

// countModelTurns 统计历史里模型回复的条数。
func countModelTurns(inner map[string]any) int {
	contents, _ := inner["contents"].([]any)
	n := 0
	for _, c := range contents {
		if content, ok := c.(map[string]any); ok && content["role"] == "model" {
			n++
		}
	}
	return n
}

// derivedUUID 由种子稳定派生一个 UUID v4 格式的字符串。
func derivedUUID(kind, seed string) string {
	sum := sha256.Sum256([]byte(conversationSalt + kind + "\x00" + seed))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x40 // 版本 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 变体
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
