package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// openaiHeaders 把 importAndKey 给的 Gemini 头换成 OpenAI SDK 的写法（Authorization: Bearer）。
func openaiHeaders(t *testing.T, srv *httptest.Server) map[string]string {
	h := importAndKey(t, srv)
	return map[string]string{"Authorization": "Bearer " + h["x-goog-api-key"]}
}

func TestChatCompletionsNonStream(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := openaiHeaders(t, srv)

	resp := doReq(t, srv, http.MethodPost, "/v1/chat/completions", h,
		`{"model":"gemini-3-flash","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello there"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(resp))
	}
	var c struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	decode(t, resp, &c)
	if c.Object != "chat.completion" || c.Model != "gemini-3-flash" || len(c.Choices) != 1 {
		t.Fatalf("completion = %+v", c)
	}
	if ch := c.Choices[0]; ch.Message.Role != "assistant" || ch.Message.Content != "echo: hello there" || ch.FinishReason != "stop" {
		t.Fatalf("choice = %+v", ch)
	}
	if c.Usage.PromptTokens != 2 || c.Usage.CompletionTokens != 3 || c.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", c.Usage)
	}
}

func TestChatCompletionsStream(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := openaiHeaders(t, srv)

	resp := doReq(t, srv, http.MethodPost, "/v1/chat/completions", h,
		`{"model":"gemini-3-flash","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":[{"type":"text","text":"stream me"}]}]}`)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d %s: %s", resp.StatusCode, resp.Header.Get("Content-Type"), readBody(resp))
	}
	body := readBody(resp)
	var text strings.Builder
	var finish string
	var promptTokens float64
	done := false
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			done = true
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens float64 `json:"prompt_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatalf("bad chunk %q: %v", data, err)
		}
		for _, ch := range c.Choices {
			text.WriteString(ch.Delta.Content)
			if ch.FinishReason != nil {
				finish = *ch.FinishReason
			}
		}
		if c.Usage != nil {
			promptTokens = c.Usage.PromptTokens
		}
	}
	if text.String() != "echo: stream me" || finish != "stop" || promptTokens != 2 || !done {
		t.Fatalf("text %q finish %q prompt %v done %v\nbody:\n%s", text.String(), finish, promptTokens, done, body)
	}
}

// /v1/models：不带区分头（OpenAI SDK）是 OpenAI 格式；带 x-goog-api-key 或 ?key= 是 Gemini 格式；/v1beta 不变。
func TestOpenAIModels(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := openaiHeaders(t, srv)
	key := strings.TrimPrefix(h["Authorization"], "Bearer ")

	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/v1/models", h, ""), &list)
	if list.Object != "list" || len(list.Data) != 3 || list.Data[0].Object != "model" || list.Data[0].OwnedBy == "" {
		t.Fatalf("openai models = %+v", list)
	}
	var one struct {
		ID     string `json:"id"`
		Object string `json:"object"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/v1/models/gemini-3-flash", h, ""), &one)
	if one.ID != "gemini-3-flash" || one.Object != "model" {
		t.Fatalf("openai model = %+v", one)
	}

	for _, tc := range []struct {
		path    string
		headers map[string]string
	}{
		{"/v1/models", map[string]string{"x-goog-api-key": key}},
		{"/v1/models?key=" + key, nil},
		{"/v1beta/models", h},
	} {
		var gemini struct {
			Models []map[string]any `json:"models"`
		}
		decode(t, doReq(t, srv, http.MethodGet, tc.path, tc.headers, ""), &gemini)
		if len(gemini.Models) != 3 {
			t.Fatalf("%s: gemini models = %+v", tc.path, gemini)
		}
	}
}

// 错误都是 OpenAI 的格式：没带 Key 401、请求不合法 400、模型不存在 404；Gemini 接口的错误格式不变。
func TestChatCompletionsErrors(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := openaiHeaders(t, srv)
	check := func(resp *http.Response, status int, errType string) {
		t.Helper()
		var body struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		decode(t, resp, &body)
		if resp.StatusCode != status || body.Error.Type != errType || body.Error.Message == "" {
			t.Fatalf("status %d body %+v，应为 %d %s", resp.StatusCode, body, status, errType)
		}
	}
	msg := `{"model":"gemini-3-flash","messages":[{"role":"user","content":"x"}]}`
	check(doReq(t, srv, http.MethodPost, "/v1/chat/completions", nil, msg), http.StatusUnauthorized, "authentication_error")
	check(doReq(t, srv, http.MethodGet, "/v1/models", nil, ""), http.StatusUnauthorized, "authentication_error")
	check(doReq(t, srv, http.MethodPost, "/v1/chat/completions", h, `{"model":"gemini-3-flash","messages":[]}`),
		http.StatusBadRequest, "invalid_request_error")
	check(doReq(t, srv, http.MethodPost, "/v1/chat/completions", h, strings.Replace(msg, "gemini-3-flash", "no-such-model", 1)),
		http.StatusNotFound, "not_found_error")

	resp := doReq(t, srv, http.MethodPost, "/v1/models/gemini-3-flash:generateContent", map[string]string{"Authorization": "Bearer nope"}, `{}`)
	if body := readBody(resp); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `"status":"UNAUTHENTICATED"`) {
		t.Fatalf("gemini error = %d %s", resp.StatusCode, body)
	}
}

// 用量记 openai 接口和客户端原来的模型名。
func TestChatCompletionsUsageProtocol(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	h := openaiHeaders(t, srv)

	resp := doReq(t, srv, http.MethodPost, "/v1/chat/completions", h, `{"model":"gemini-3-flash","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(resp))
	}
	_ = readBody(resp)
	svc.Close(t.Context()) // 写完排队的用量记录
	var logs struct {
		Records []struct {
			RequestModel string `json:"request_model"`
			Protocol     string `json:"protocol"`
		} `json:"records"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/logs?protocol=openai", nil, ""), &logs)
	if len(logs.Records) != 1 || logs.Records[0].RequestModel != "gemini-3-flash" || logs.Records[0].Protocol != "openai" {
		t.Fatalf("usage = %+v", logs.Records)
	}
}
