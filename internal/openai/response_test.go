package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

type completion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role             string  `json:"role"`
			Content          *string `json:"content"`
			ReasoningContent string  `json:"reasoning_content"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

func convert(t *testing.T, raw string) completion {
	t.Helper()
	out, err := ConvertResponse([]byte(raw), "gemini-3-flash")
	if err != nil {
		t.Fatal(err)
	}
	var c completion
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// 思考进 reasoning_content，文字拼起来，用量按 OpenAI 口径（输入含缓存、输出含思考）。
func TestConvertResponseText(t *testing.T) {
	c := convert(t, `{"candidates":[{"content":{"parts":[
		{"text":"thinking...","thought":true},{"text":"","thought":true,"thoughtSignature":"sig"},
		{"text":"Hello"},{"text":", world"}]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":30,"cachedContentTokenCount":60}}`)
	if !strings.HasPrefix(c.ID, "chatcmpl-") || c.Object != "chat.completion" || c.Model != "gemini-3-flash" || len(c.Choices) != 1 {
		t.Fatalf("completion = %+v", c)
	}
	m := c.Choices[0].Message
	if m.Role != "assistant" || m.Content == nil || *m.Content != "Hello, world" || m.ReasoningContent != "thinking..." || c.Choices[0].FinishReason != "stop" {
		t.Fatalf("message = %+v finish = %s", m, c.Choices[0].FinishReason)
	}
	u := c.Usage
	if u.PromptTokens != 100 || u.CompletionTokens != 50 || u.TotalTokens != 150 ||
		u.PromptTokensDetails.CachedTokens != 60 || u.CompletionTokensDetails.ReasoningTokens != 30 {
		t.Fatalf("usage = %+v", u)
	}
}

// 工具调用：上游给了 id 就用，没给就生成 call_；只有工具调用时 content 是 null。
func TestConvertResponseToolCalls(t *testing.T) {
	c := convert(t, `{"candidates":[{"content":{"parts":[
		{"functionCall":{"name":"get_weather","args":{
			"city": "Paris"
		}},"thoughtSignature":"sig"},
		{"functionCall":{"id":"toolu_vrtx_01abc","name":"now"}}]},"finishReason":"STOP"}]}`)
	m := c.Choices[0].Message
	if m.Content != nil || len(m.ToolCalls) != 2 || c.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("message = %+v", m)
	}
	first, second := m.ToolCalls[0], m.ToolCalls[1]
	if !strings.HasPrefix(first.ID, "call_") || len(first.ID) != 29 || first.Type != "function" ||
		first.Function.Name != "get_weather" || first.Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("first = %+v", first)
	}
	if second.ID != "toolu_vrtx_01abc" || second.Function.Arguments != `{}` {
		t.Fatalf("second = %+v", second)
	}
}

func TestConvertResponseFinishReasons(t *testing.T) {
	for raw, want := range map[string]string{
		`{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"MAX_TOKENS"}]}`: "length",
		`{"candidates":[{"content":{"parts":[]},"finishReason":"SAFETY"}]}`:                 "content_filter",
		`{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"}}`:                           "content_filter",
	} {
		c := convert(t, raw)
		if got := c.Choices[0].FinishReason; got != want {
			t.Errorf("%s → %s，应为 %s", raw, got, want)
		}
		if c.Choices[0].Message.Content == nil {
			t.Errorf("%s：没有工具调用时 content 应是字符串", raw)
		}
	}
}

// 生图模型的图片放进 message.images（data URL，index 是第几张）；思考里的图片不返回；只有图片时 content 是空字符串。
func TestConvertResponseImages(t *testing.T) {
	out, err := ConvertResponse([]byte(`{"candidates":[{"content":{"parts":[
		{"inlineData":{"mimeType":"image/png","data":"dGhpbms="},"thought":true},
		{"inlineData":{"mimeType":"image/jpeg","data":"aW1nMQ=="},"thoughtSignature":"sig"},
		{"inlineData":{"data":"aW1nMg=="}}]},"finishReason":"STOP"}]}`), "gemini-3.1-flash-image")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
				Images  []struct {
					Index    int    `json:"index"`
					Type     string `json:"type"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatal(err)
	}
	m := c.Choices[0].Message
	if m.Content == nil || *m.Content != "" || len(m.Images) != 2 {
		t.Fatalf("message = %s", out)
	}
	if m.Images[0].Index != 0 || m.Images[0].Type != "image_url" || m.Images[0].ImageURL.URL != "data:image/jpeg;base64,aW1nMQ==" ||
		m.Images[1].Index != 1 || m.Images[1].ImageURL.URL != "data:image/png;base64,aW1nMg==" {
		t.Fatalf("images = %+v", m.Images)
	}
}
