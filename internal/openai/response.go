package openai

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"time"
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
	Text         string `json:"text"`
	Thought      bool   `json:"thought"`
	FunctionCall *struct {
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

// Usage 是 OpenAI 的用量：prompt_tokens 含命中缓存的部分，completion_tokens 含思考。
type Usage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func usageFrom(u *geminiUsage) Usage {
	var out Usage
	if u == nil {
		return out
	}
	out.PromptTokens = u.PromptTokenCount
	out.CompletionTokens = u.CandidatesTokenCount + u.ThoughtsTokenCount
	out.TotalTokens = out.PromptTokens + out.CompletionTokens
	out.PromptTokensDetails.CachedTokens = u.CachedContentTokenCount
	out.CompletionTokensDetails.ReasoningTokens = u.ThoughtsTokenCount
	return out
}

// finishReason 把 Gemini 的 finishReason 换成 OpenAI 的。有工具调用时是 tool_calls。
func finishReason(finish string, toolUse bool) string {
	switch {
	case toolUse:
		return "tool_calls"
	case finish == "MAX_TOKENS":
		return "length"
	case finish == "SAFETY", finish == "RECITATION", finish == "PROHIBITED_CONTENT", finish == "SPII",
		finish == "BLOCKLIST", finish == "IMAGE_SAFETY":
		return "content_filter"
	default:
		return "stop"
	}
}

// toolCall 是 OpenAI 的一个工具调用。arguments 是 JSON 字符串，压成一行（上游非流式的参数带缩进）。
func toolCall(p geminiPart) map[string]any {
	args := argsOrEmpty(p.FunctionCall.Args)
	var buf bytes.Buffer
	if json.Compact(&buf, args) == nil {
		args = buf.Bytes()
	}
	return map[string]any{"id": toolID(p.FunctionCall.ID), "type": "function",
		"function": map[string]any{"name": p.FunctionCall.Name, "arguments": string(args)}}
}

// ConvertResponse 把非流式 Gemini 响应转成 chat.completion。model 原样回传客户端请求的模型名。
func ConvertResponse(raw []byte, model string) ([]byte, error) {
	var g geminiResponse
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, err
	}
	var text, reasoning []byte
	var calls []any
	finish := ""
	if len(g.Candidates) > 0 {
		cand := g.Candidates[0]
		finish = cand.FinishReason
		for _, p := range cand.Content.Parts {
			switch {
			case p.Thought:
				reasoning = append(reasoning, p.Text...)
			case p.FunctionCall != nil:
				calls = append(calls, toolCall(p))
			default:
				text = append(text, p.Text...)
			}
		}
	} else if g.PromptFeedback != nil && g.PromptFeedback.BlockReason != "" {
		finish = "SAFETY"
	}
	message := map[string]any{"role": "assistant", "content": string(text)}
	if len(reasoning) > 0 {
		message["reasoning_content"] = string(reasoning)
	}
	if len(calls) > 0 {
		message["tool_calls"] = calls
		if len(text) == 0 {
			message["content"] = nil
		}
	}
	return json.Marshal(map[string]any{
		"id":      NewCompletionID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "message": message, "logprobs": nil,
			"finish_reason": finishReason(finish, len(calls) > 0)}},
		"usage": usageFrom(g.UsageMetadata),
	})
}

// argsOrEmpty 返回工具参数，缺省时是空对象。
func argsOrEmpty(args json.RawMessage) json.RawMessage {
	if len(args) == 0 || string(args) == "null" {
		return json.RawMessage("{}")
	}
	return args
}

// toolID 返回工具调用 id：上游给了就用（Claude 是 toolu_vrtx_…），没给（Gemini）就生成 OpenAI 形态的 call_ 加 24 位。
func toolID(id string) string {
	if id != "" {
		return id
	}
	return "call_" + randomString(24)
}

// NewCompletionID 生成 chat.completion 的 id（chatcmpl- 加 29 位，同 OpenAI 的形态）。
func NewCompletionID() string { return "chatcmpl-" + randomString(29) }

const alphanumeric = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func randomString(n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphanumeric)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err) // crypto/rand 不会失败
		}
		b[i] = alphanumeric[v.Int64()]
	}
	return string(b)
}
