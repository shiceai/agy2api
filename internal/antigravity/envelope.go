package antigravity

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// skipThoughtSignature 是工具调用 thoughtSignature 的兜底占位值，
// 避免上游因缺签名拒绝多轮工具调用。
const skipThoughtSignature = "skip_thought_signature_validator"

// 信封的 userAgent 字段（与请求头的 User-Agent 不同）：Gmail 个人号是 antigravity，
// 企业、教育等非 Gmail 账号是 jetski，同时 request.metadata.ideType 为 JETSKI。
const (
	bodyUserAgent       = "antigravity"
	bodyUserAgentJetski = "jetski"
)

// isGmail 判断是否 Gmail 个人号；邮箱未知时按个人号处理。
func isGmail(email string) bool {
	e := strings.ToLower(strings.TrimSpace(email))
	return e == "" || strings.HasSuffix(e, "@gmail.com") || strings.HasSuffix(e, "@googlemail.com")
}

// sessionSalt 让派生的 sessionId 在不同部署间不同，避免成为账号池指纹。
var sessionSalt = []byte("agy2api:antigravity:session\x00")

// BuildParams 是构造一次生成请求信封所需的参数。
type BuildParams struct {
	ProjectID string
	// WireModel 是发给上游的模型 ID。
	WireModel string
	// AccountID 用于派生账号稳定的 sessionId。
	AccountID int64
	// GeminiRequest 是客户端的原生 Gemini 请求体（contents/tools/generationConfig 等）。
	GeminiRequest map[string]any
	// Model 是该模型在目录中的信息，用于补齐思考配置默认值和 labels.model_enum；可为空。
	Model *ModelInfo
	// Caller 标识调用方（如 API Key），和账号、首条用户消息一起派生对话的会话 ID、轨迹 ID。
	Caller string
	// Email 是账号邮箱，非 Gmail 账号用 jetski 标识。
	Email string
	// HideThoughts 为真时请求侧关闭思考内容返回（thinkingConfig.includeThoughts=false），模型仍思考但不回思考摘要。
	HideThoughts bool
}

// BuildEnvelope 把客户端的 Gemini 请求包装成 Cloud Code v1internal 信封：request 里补上账号稳定的
// sessionId 和对齐官方客户端的 labels，信封带官方格式的 requestId。
func BuildEnvelope(p BuildParams) ([]byte, error) {
	inner := buildRequest(p)
	conv := conversationFor(p.AccountID, p.Caller, inner)
	inner["sessionId"] = sessionIDForAccount(p.AccountID)
	inner["labels"] = conv.labels(p.WireModel, p.Model)
	userAgent := bodyUserAgent
	if !isGmail(p.Email) {
		userAgent = bodyUserAgentJetski
		inner["metadata"] = map[string]any{"ideType": "JETSKI"}
	}

	envelope := map[string]any{
		"project":     p.ProjectID,
		"requestId":   conv.requestID(time.Now()),
		"request":     inner,
		"model":       p.WireModel,
		"userAgent":   userAgent,
		"requestType": "agent",
	}
	return json.Marshal(envelope)
}

// buildRequest 在客户端请求体的副本上做转换，得到发给上游的 request 对象（不含会话字段）。
func buildRequest(p BuildParams) map[string]any {
	inner := deepCopyMap(p.GeminiRequest)
	delete(inner, "model")

	normalizeRequest(inner)
	// CLI 不发送 safetySettings。
	delete(inner, "safetySettings")
	adaptForModel(inner, p)

	sanitizeTools(inner)
	stubToolThoughtSignatures(inner)
	if IsTierModel(p.WireModel) {
		stripTierThinking(inner)
	}
	applyCatalogDefaults(inner, p.Model)
	if p.HideThoughts {
		hideThoughts(inner)
	}
	return inner
}

// hideThoughts 关闭思考内容返回：有 thinkingConfig 时把 includeThoughts 置 false（模型仍按预算思考，只是不回摘要）。
// 没有 thinkingConfig 的请求本就不会回思考，不处理。
func hideThoughts(inner map[string]any) {
	gen, _ := inner["generationConfig"].(map[string]any)
	tc, ok := gen["thinkingConfig"].(map[string]any)
	if !ok {
		return
	}
	tc["includeThoughts"] = false
}

// BuildCountTokensBody 构造 countTokens 请求体：只要 request 对象，不带会话字段和 toolConfig。
func BuildCountTokensBody(p BuildParams) ([]byte, error) {
	inner := buildRequest(p)
	delete(inner, "toolConfig")
	return json.Marshal(map[string]any{"request": inner})
}

// stripTierThinking 去掉客户端给的思考强度（thinkingLevel、thinkingBudget）：分档模型的档位已经
// 体现在模型名里，再带强度参数可能与档位冲突。includeThoughts 保留；thinkingConfig 因此变空时整个删掉，
// 交给目录默认值。
// 调用前请求体已经过 normalizeRequest，字段名都是 camelCase。
func stripTierThinking(inner map[string]any) {
	gen, _ := inner["generationConfig"].(map[string]any)
	tc, ok := gen["thinkingConfig"].(map[string]any)
	if !ok {
		return
	}
	delete(tc, "thinkingLevel")
	delete(tc, "thinkingBudget")
	if len(tc) == 0 {
		delete(gen, "thinkingConfig")
	}
}

// applyCatalogDefaults 按目录补齐 generationConfig 默认值，对齐官方客户端（恒发 maxOutputTokens，
// 目录有思考预算时带 thinkingConfig）；客户端已指定的不覆盖。
func applyCatalogDefaults(inner map[string]any, model *ModelInfo) {
	if model == nil {
		return
	}
	// 调用前请求体已经过 normalizeRequest，字段名都是 camelCase。
	gen, _ := inner["generationConfig"].(map[string]any)
	if gen == nil {
		gen = map[string]any{}
		inner["generationConfig"] = gen
	}
	if _, ok := gen["maxOutputTokens"]; !ok && model.MaxOutputTokens != nil {
		gen["maxOutputTokens"] = *model.MaxOutputTokens
	}
	// 客户端已显式指定则不覆盖。
	if _, ok := gen["thinkingConfig"]; ok {
		return
	}
	if model.SupportsThinking && model.ThinkingBudget != nil {
		gen["thinkingConfig"] = map[string]any{
			"includeThoughts": true,
			"thinkingBudget":  *model.ThinkingBudget,
		}
	}
}

// sessionIDForAccount 派生账号稳定的 sessionId：模仿官方客户端的"同一安装"稳定负 int64。
func sessionIDForAccount(accountID int64) string {
	seed := append(append([]byte(nil), sessionSalt...), []byte(strconv.FormatInt(accountID, 10))...)
	sum := sha256.Sum256(seed)
	value := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7FFFFFFFFFFFFFFF)
	return "-" + strconv.FormatInt(value, 10)
}

// deepCopyMap 深拷贝 JSON 解码出的请求体。值只会是 map、切片和不可变的标量，递归复制前两者即可。
func deepCopyMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopyMap(t)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = deepCopyValue(x)
		}
		return out
	default:
		return v
	}
}

// stubToolThoughtSignatures 为出站 functionCall 补占位 thoughtSignature，
// 并清理入站 functionResponse 上可能过期的签名。
func stubToolThoughtSignatures(inner map[string]any) {
	contents, ok := inner["contents"].([]any)
	if !ok {
		return
	}
	for _, rawContent := range contents {
		content, ok := rawContent.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := content["parts"].([]any)
		if !ok {
			continue
		}
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			if _, hasResp := part["functionResponse"]; hasResp {
				delete(part, "thoughtSignature")
				delete(part, "thought_signature")
				continue
			}
			if _, hasCall := part["functionCall"]; hasCall {
				if !hasThoughtSignature(part) {
					part["thoughtSignature"] = skipThoughtSignature
				}
			}
		}
	}
}

func hasThoughtSignature(part map[string]any) bool {
	if v, ok := part["thoughtSignature"].(string); ok && strings.TrimSpace(v) != "" {
		return true
	}
	if v, ok := part["thought_signature"].(string); ok && strings.TrimSpace(v) != "" {
		return true
	}
	return false
}
