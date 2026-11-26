// Package mockupstream 提供一个模拟的 Cloud Code 上游，用于联调与测试，
// 覆盖 OAuth 刷新、userinfo、loadCodeAssist、fetchAvailableModels 和生成接口。
// 它不代表真实上游的全部行为，只保证核心链路可端到端跑通。
package mockupstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Handler 返回模拟上游的 http.Handler。
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", handleToken)
	mux.HandleFunc("/oauth2/v2/userinfo", handleUserInfo)
	mux.HandleFunc("/v1internal:loadCodeAssist", handleLoadCodeAssist)
	mux.HandleFunc("/v1internal:onboardUser", handleOnboard)
	mux.HandleFunc("/v1internal:fetchAvailableModels", handleFetchModels)
	mux.HandleFunc("/v1internal:generateContent", handleGenerate)
	mux.HandleFunc("/v1internal:streamGenerateContent", handleStreamGenerate)
	mux.HandleFunc("/v1internal:countTokens", handleCountTokens)
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func handleToken(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"access_token": "mock-access-" + fmt.Sprint(time.Now().UnixNano()),
		"expires_in":   3600,
		"token_type":   "Bearer",
		"scope":        "https://www.googleapis.com/auth/cloud-platform",
	}
	// Google 登录用授权码换令牌时下发 refresh token。
	if r.PostFormValue("grant_type") == "authorization_code" {
		resp["refresh_token"] = "mock-refresh-from-code"
	}
	writeJSON(w, resp)
}

func handleUserInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"email":          "mock-user@example.com",
		"name":           "Mock User",
		"verified_email": true,
	})
}

func handleLoadCodeAssist(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"cloudaicompanionProject": "mock-project",
		"currentTier":             map[string]any{"id": "free-tier", "name": "Free", "isDefault": true},
		"allowedTiers":            []any{map[string]any{"id": "free-tier", "name": "Free", "isDefault": true}},
	})
}

func handleOnboard(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"done":     true,
		"response": map[string]any{"cloudaicompanionProject": "mock-project"},
	})
}

func handleFetchModels(w http.ResponseWriter, r *http.Request) {
	budget := 8192
	maxOut := 65536
	writeJSON(w, map[string]any{
		"models": map[string]any{
			"gemini-3-pro-high": map[string]any{
				"displayName":      "Gemini 3 Pro (High)",
				"model":            "MODEL_PLACEHOLDER_M101",
				"supportsThinking": true,
				"thinkingBudget":   budget,
				"maxOutputTokens":  maxOut,
				"quotaInfo":        map[string]any{"remainingFraction": 0.87, "resetTime": time.Now().Add(5 * time.Hour).UTC().Format(time.RFC3339)},
			},
			"gemini-3-pro-low": map[string]any{
				"displayName":      "Gemini 3 Pro (Low)",
				"model":            "MODEL_PLACEHOLDER_M102",
				"supportsThinking": true,
				"maxOutputTokens":  maxOut,
			},
			"gemini-3-flash-high": map[string]any{
				"displayName":      "Gemini 3 Flash (High)",
				"model":            "MODEL_PLACEHOLDER_M103",
				"supportsThinking": true,
				"thinkingBudget":   budget,
				"maxOutputTokens":  maxOut,
				"quotaInfo":        map[string]any{"remainingFraction": 0.95, "resetTime": time.Now().Add(5 * time.Hour).UTC().Format(time.RFC3339)},
			},
		},
		// 官方客户端挑模型用的信息：选择器里有 gemini-3-pro 的两档，gemini-3-pro 按档位分组；gemini-3-flash-high
		// 是辅助用途（命令）的模型。对外列表因此是 gemini-3-pro 和 gemini-3-flash-high。
		"agentModelSorts": []any{map[string]any{"displayName": "Recommended", "groups": []any{
			map[string]any{"modelIds": []any{"gemini-3-pro-high", "gemini-3-pro-low"}},
		}}},
		"groupedModels": map[string]any{"gemini-3-pro": map[string]any{"displayName": "Gemini 3 Pro", "supportedThinkingLevels": []any{
			map[string]any{"thinkingSuffix": "low", "thinkingDisplayName": "Low"},
			map[string]any{"thinkingSuffix": "high", "thinkingDisplayName": "High"},
		}}},
		"commandModelIds": []any{"gemini-3-flash-high"},
	})
}

// handleGenerate 回显一段文本，并带上用量，验证非流式解包。
func handleGenerate(w http.ResponseWriter, r *http.Request) {
	var env map[string]any
	_ = json.NewDecoder(r.Body).Decode(&env)
	prompt := firstUserText(env)
	writeJSON(w, map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": "echo: " + prompt}}},
					"finishReason": "STOP",
				},
			},
			"modelVersion": "gemini-3-flash-high",
			"usageMetadata": map[string]any{
				"promptTokenCount":     len(strings.Fields(prompt)),
				"candidatesTokenCount": 3,
				"totalTokenCount":      len(strings.Fields(prompt)) + 3,
			},
		},
	})
}

// handleStreamGenerate 发送两条内容帧和一条带用量的终止帧。
func handleStreamGenerate(w http.ResponseWriter, r *http.Request) {
	var env map[string]any
	_ = json.NewDecoder(r.Body).Decode(&env)
	prompt := firstUserText(env)

	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)

	frames := []map[string]any{
		{"response": map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": "echo: "}}}}}, "modelVersion": "gemini-3-flash-high"}},
		{"response": map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": prompt}}}}}, "modelVersion": "gemini-3-flash-high"}},
		{"response": map[string]any{
			"candidates":    []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{}}, "finishReason": "STOP"}},
			"modelVersion":  "gemini-3-flash-high",
			"usageMetadata": map[string]any{"promptTokenCount": len(strings.Fields(prompt)), "candidatesTokenCount": 3, "totalTokenCount": len(strings.Fields(prompt)) + 3},
		}},
	}
	for _, f := range frames {
		raw, _ := json.Marshal(f)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func handleCountTokens(w http.ResponseWriter, r *http.Request) {
	var env map[string]any
	_ = json.NewDecoder(r.Body).Decode(&env)
	prompt := firstUserText(env)
	writeJSON(w, map[string]any{
		"response": map[string]any{"totalTokens": len(strings.Fields(prompt))},
	})
}

// firstUserText 从信封里抽出第一条用户文本，用于回显。
func firstUserText(env map[string]any) string {
	req, _ := env["request"].(map[string]any)
	if req == nil {
		req = env
	}
	contents, _ := req["contents"].([]any)
	for _, c := range contents {
		m, _ := c.(map[string]any)
		if m == nil || m["role"] != "user" {
			continue
		}
		parts, _ := m["parts"].([]any)
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if t, ok := pm["text"].(string); ok && strings.TrimSpace(t) != "" {
				return t
			}
		}
	}
	return "(empty)"
}
