package anthropic

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
)

// geminiResponse 是 Gemini 响应（非流式整体、流式每一块）里用到的字段。
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata  *geminiUsage `json:"usageMetadata"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
}

type geminiPart struct {
	Text             string `json:"text"`
	Thought          bool   `json:"thought"`
	ThoughtSignature string `json:"thoughtSignature"`
	FunctionCall     *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
}

type geminiUsage struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
}

// Usage 是 Messages 的用量：输入不含命中缓存的部分，输出含思考。
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
}

func usageFrom(u *geminiUsage) Usage {
	if u == nil {
		return Usage{}
	}
	input := u.PromptTokenCount - u.CachedContentTokenCount
	if input < 0 {
		input = 0
	}
	return Usage{InputTokens: input, CacheReadInputTokens: u.CachedContentTokenCount,
		OutputTokens: u.CandidatesTokenCount + u.ThoughtsTokenCount}
}

// skipThoughtSignature 是网关自己补的占位签名（见 antigravity 包），不是上游给的，不回给客户端。
const skipThoughtSignature = "skip_thought_signature_validator"

// stopReason 把 Gemini 的 finishReason 换成 Messages 的 stop_reason。有工具调用时是 tool_use。
func stopReason(finish string, toolUse bool) string {
	switch {
	case toolUse:
		return "tool_use"
	case finish == "MAX_TOKENS":
		return "max_tokens"
	case finish == "SAFETY", finish == "RECITATION", finish == "PROHIBITED_CONTENT", finish == "SPII",
		finish == "BLOCKLIST", finish == "IMAGE_SAFETY":
		return "refusal"
	default:
		return "end_turn"
	}
}

// ConvertResponse 把非流式 Gemini 响应转成 Messages 响应。model 原样回传客户端请求的模型名。
func ConvertResponse(raw []byte, model string) ([]byte, error) {
	var g geminiResponse
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, err
	}
	content := []any{}
	toolUse := false
	finish := ""
	if len(g.Candidates) > 0 {
		cand := g.Candidates[0]
		finish = cand.FinishReason
		var thinking map[string]any // 正在拼的思考块：连续的思考 part 合成一块
		flush := func() {
			if thinking != nil {
				content = append(content, thinking)
				thinking = nil
			}
		}
		for _, p := range cand.Content.Parts {
			switch {
			case p.Thought:
				if p.Text == "" && (p.ThoughtSignature == "" || p.ThoughtSignature == skipThoughtSignature) {
					continue // 空的思考 part，不出空块
				}
				if thinking == nil {
					thinking = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
				}
				thinking["thinking"] = thinking["thinking"].(string) + p.Text
				if p.ThoughtSignature != "" && p.ThoughtSignature != skipThoughtSignature {
					thinking["signature"] = p.ThoughtSignature
					flush() // 签名在思考的最后一块上
				}
			case p.FunctionCall != nil:
				flush()
				if sig := p.ThoughtSignature; sig != "" && sig != skipThoughtSignature {
					content = append(content, map[string]any{"type": "thinking", "thinking": "", "signature": sig})
				}
				toolUse = true
				content = append(content, map[string]any{"type": "tool_use", "id": toolID(p.FunctionCall.ID),
					"name": p.FunctionCall.Name, "input": argsOrEmpty(p.FunctionCall.Args)})
			case p.Text != "":
				flush()
				if n := len(content); n > 0 {
					if last, ok := content[n-1].(map[string]any); ok && last["type"] == "text" {
						last["text"] = last["text"].(string) + p.Text
						continue
					}
				}
				content = append(content, map[string]any{"type": "text", "text": p.Text})
			}
		}
		flush()
	} else if g.PromptFeedback != nil && g.PromptFeedback.BlockReason != "" {
		finish = "SAFETY"
	}
	return json.Marshal(map[string]any{
		"id":            NewMessageID(),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stopReason(finish, toolUse),
		"stop_sequence": nil,
		"usage":         usageFrom(g.UsageMetadata),
	})
}

// argsOrEmpty 返回工具参数，缺省时是空对象。
func argsOrEmpty(args json.RawMessage) json.RawMessage {
	if len(args) == 0 || string(args) == "null" {
		return json.RawMessage("{}")
	}
	return args
}

// toolID 返回工具调用 id：上游给了就用（Claude 是 toolu_vrtx_…），没给（Gemini）就生成一个同样形态的。
func toolID(id string) string {
	if id != "" {
		return id
	}
	return "toolu_vrtx_01" + randomBase58(22)
}

// NewMessageID 生成消息 id，形态同上游（msg_vrtx_01 加 22 位 base58）。
func NewMessageID() string { return "msg_vrtx_01" + randomBase58(22) }

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func randomBase58(n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(base58Alphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err) // crypto/rand 不会失败
		}
		b[i] = base58Alphabet[v.Int64()]
	}
	return string(b)
}
