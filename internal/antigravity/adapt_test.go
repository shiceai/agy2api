package antigravity

import (
	"encoding/json"
	"testing"
)

func adaptedRequest(t *testing.T, model, body string) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	return buildRequest(BuildParams{ProjectID: "p", WireModel: model, GeminiRequest: req})
}

// Gemini 去掉 penalty，保留 stopSequences；Claude 的这些参数原样保留。
func TestAdaptPenalties(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"generationConfig":{"presencePenalty":0.5,"frequency_penalty":0.5,"stopSequences":["END"],"temperature":1}}`
	gemini := mustJSON(t, adaptedRequest(t, "gemini-2.5-flash", body)["generationConfig"])
	if gemini != `{"stopSequences":["END"],"temperature":1}` {
		t.Fatalf("Gemini 应只去掉 penalty：%s", gemini)
	}
	claude := mustJSON(t, adaptedRequest(t, "claude-sonnet-5-5-low", body)["generationConfig"])
	if claude != `{"frequencyPenalty":0.5,"presencePenalty":0.5,"stopSequences":["END"],"temperature":1}` {
		t.Fatalf("Claude 的参数应保留：%s", claude)
	}
}

// Claude 去掉末尾的模型消息（不支持预填充）；Gemini 保留。
func TestAdaptDropsClaudePrefill(t *testing.T) {
	body := `{"contents":[
		{"role":"user","parts":[{"text":"count"}]},
		{"role":"model","parts":[{"text":"1,"}]},
		{"role":"model","parts":[{"text":"2,"}]}]}`
	claude := adaptedRequest(t, "claude-opus-5-5-high", body)["contents"].([]any)
	if len(claude) != 1 || claude[0].(map[string]any)["role"] != "user" {
		t.Fatalf("Claude 应去掉末尾的模型消息：%s", mustJSON(t, claude))
	}
	if gemini := adaptedRequest(t, "gemini-2.5-flash", body)["contents"].([]any); len(gemini) != 3 {
		t.Fatalf("Gemini 应保留预填充：%s", mustJSON(t, gemini))
	}
}

// Claude 给缺 id 的工具调用补 id，并按名字和顺序与工具结果配对；已有 id 的不动。
func TestAdaptAssignsToolCallIDs(t *testing.T) {
	body := `{"contents":[
		{"role":"user","parts":[{"text":"weather in Paris and Rome, then time"}]},
		{"role":"model","parts":[
			{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}},
			{"functionCall":{"name":"get_weather","args":{"city":"Rome"}}},
			{"functionCall":{"id":"client_1","name":"get_time","args":{}}}]},
		{"role":"user","parts":[
			{"functionResponse":{"id":"client_1","name":"get_time","response":{"t":"noon"}}},
			{"functionResponse":{"name":"get_weather","response":{"temp":18}}},
			{"functionResponse":{"name":"get_weather","response":{"temp":20}}}]},
		{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Oslo"}}}]},
		{"role":"user","parts":[{"function_response":{"name":"get_weather","response":{"temp":5}}}]}]}`
	contents := adaptedRequest(t, "claude-sonnet-5-5-low", body)["contents"].([]any)
	id := func(content, part int, kind string) any {
		parts := contents[content].(map[string]any)["parts"].([]any)
		return parts[part].(map[string]any)[kind].(map[string]any)["id"]
	}
	checks := []struct {
		content, part int
		kind, want    string
	}{
		{1, 0, "functionCall", "toolu_q2a_1"},
		{1, 1, "functionCall", "toolu_q2a_2"},
		{1, 2, "functionCall", "client_1"},
		{2, 0, "functionResponse", "client_1"},
		{2, 1, "functionResponse", "toolu_q2a_1"},
		{2, 2, "functionResponse", "toolu_q2a_2"},
		{3, 0, "functionCall", "toolu_q2a_3"},
		{4, 0, "functionResponse", "toolu_q2a_3"}, // snake_case 也已归一
	}
	for _, c := range checks {
		if got := id(c.content, c.part, c.kind); got != c.want {
			t.Errorf("contents[%d].parts[%d].%s.id 应为 %s，实际 %v", c.content, c.part, c.kind, c.want, got)
		}
	}

	// 同一段历史每次补出的 id 相同。
	again := adaptedRequest(t, "claude-sonnet-5-5-low", body)["contents"]
	if mustJSON(t, again) != mustJSON(t, contents) {
		t.Fatal("同一段历史应补出相同的 id")
	}
	// Gemini 不补 id。
	gemini := adaptedRequest(t, "gemini-3.8-flash-low", body)["contents"].([]any)
	call := gemini[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	if _, ok := call["id"]; ok {
		t.Fatalf("Gemini 不应补 id：%v", call)
	}
}
