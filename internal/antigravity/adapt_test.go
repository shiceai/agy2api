package antigravity

import (
	"encoding/json"
	"regexp"
	"strings"
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
	claudeContents := func(body string) []any {
		return adaptedRequest(t, "claude-sonnet-5-5-low", body)["contents"].([]any)
	}
	id := func(contents []any, content, part int, kind string) any {
		parts := contents[content].(map[string]any)["parts"].([]any)
		return parts[part].(map[string]any)[kind].(map[string]any)["id"]
	}
	contents := claudeContents(body)
	call1, call2, call3 := id(contents, 1, 0, "functionCall"), id(contents, 1, 1, "functionCall"), id(contents, 3, 0, "functionCall")
	// 形态照上游实抓：toolu_vrtx_01 加 22 位 base58。
	upstreamID := regexp.MustCompile(`^toolu_vrtx_01[1-9A-HJ-NP-Za-km-z]{22}$`)
	for _, v := range []any{call1, call2, call3} {
		if s, _ := v.(string); !upstreamID.MatchString(s) {
			t.Fatalf("补出的 id 形态不对：%v", v)
		}
	}
	if call1 == call2 || call2 == call3 || call1 == call3 {
		t.Fatalf("补出的 id 应互不相同：%v %v %v", call1, call2, call3)
	}
	checks := []struct {
		content, part int
		kind          string
		want          any
	}{
		{1, 2, "functionCall", "client_1"},
		{2, 0, "functionResponse", "client_1"},
		{2, 1, "functionResponse", call1},
		{2, 2, "functionResponse", call2},
		{4, 0, "functionResponse", call3}, // snake_case 也已归一
	}
	for _, c := range checks {
		if got := id(contents, c.content, c.part, c.kind); got != c.want {
			t.Errorf("contents[%d].parts[%d].%s.id 应为 %v，实际 %v", c.content, c.part, c.kind, c.want, got)
		}
	}

	// 同一段历史每次补出的 id 相同；只到第一轮工具结果的历史（上一轮的请求）补出的 id 和完整历史里的一样。
	if mustJSON(t, claudeContents(body)) != mustJSON(t, contents) {
		t.Fatal("同一段历史应补出相同的 id")
	}
	var full map[string]any
	if err := json.Unmarshal([]byte(body), &full); err != nil {
		t.Fatal(err)
	}
	full["contents"] = full["contents"].([]any)[:3]
	earlier := claudeContents(mustJSON(t, full))
	if id(earlier, 1, 0, "functionCall") != call1 || id(earlier, 1, 1, "functionCall") != call2 {
		t.Fatal("追加历史不应改变前面补出的 id")
	}
	// 不同对话（首条用户消息不同）补出的 id 不同。
	other := claudeContents(strings.Replace(body, "weather in Paris and Rome", "weather in Oslo", 1))
	if id(other, 1, 0, "functionCall") == call1 {
		t.Fatal("不同对话应补出不同的 id")
	}
	// Gemini 不补 id。
	gemini := adaptedRequest(t, "gemini-3.8-flash-low", body)["contents"].([]any)
	call := gemini[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	if _, ok := call["id"]; ok {
		t.Fatalf("Gemini 不应补 id：%v", call)
	}
}
