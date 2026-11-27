package antigravity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// 上下文指纹：对 Gemini 请求的 contents 按轮累积哈希。第 k 个指纹覆盖前 k 条消息，所以一段上下文的指纹
// 会原样出现在之后追加了消息的请求里（对话历史只往后追加）。被上游拒绝的上下文记下最后一个指纹，之后的请求
// 只要任一前缀的指纹等于它，就说明包含这段被拒的上下文（见 relay 的被拒上下文）。
//
// 只算对话本身：每条消息的角色和 parts（文字、图片文件数据、函数调用和结果）。思考内容和思考签名不算（客户端
// 可能改动，签名是不透明数据），网关把客户端 system 转成的那条用户消息也不算（随设置变，和对话无关）；
// systemInstruction、生成参数等不在 contents 里，本来就不算。

// fingerprintSalt 只用于上下文指纹，改了会让已存的指纹全部失效。
const fingerprintSalt = "agy2api:context-fingerprint:v1\x00"

// ContextFingerprints 返回 inner（Gemini 请求体）每个对话前缀的指纹（32 位十六进制），顺序同 contents；
// 跳过的消息不产生指纹。没有可算的消息时返回空。
func ContextFingerprints(inner map[string]any) []string {
	contents, _ := inner["contents"].([]any)
	out := make([]string, 0, len(contents))
	h := sha256.Sum256([]byte(fingerprintSalt))
	for _, c := range contents {
		content, ok := c.(map[string]any)
		if !ok || isMovedSystem(content) {
			continue
		}
		parts := fingerprintParts(content["parts"])
		if len(parts) == 0 {
			continue
		}
		raw, err := json.Marshal(parts) // map 的键按字母序输出，同样的内容得到同样的字节
		if err != nil {
			continue
		}
		role, _ := content["role"].(string)
		next := sha256.New()
		next.Write(h[:])
		next.Write([]byte(role))
		next.Write([]byte{0})
		next.Write(raw)
		copy(h[:], next.Sum(nil))
		out = append(out, hex.EncodeToString(h[:16]))
	}
	return out
}

// fingerprintParts 去掉思考 part 和各 part 上的思考签名，返回参与指纹的 parts。
func fingerprintParts(v any) []any {
	parts, _ := v.([]any)
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			if s, ok := p.(string); ok {
				out = append(out, s)
			}
			continue
		}
		if thought, _ := pm["thought"].(bool); thought {
			continue
		}
		clean := make(map[string]any, len(pm))
		for k, val := range pm {
			switch k {
			case "thoughtSignature", "thought_signature", "thought":
			default:
				clean[k] = val
			}
		}
		if len(clean) > 0 {
			out = append(out, clean)
		}
	}
	return out
}
