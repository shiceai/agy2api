package antigravity

import (
	"reflect"
	"testing"
)

func TestParseFilterRules(t *testing.T) {
	rules := ParseFilterRules("  Cline  \n# 注释\n\nRoo Code => MyAgent\nFoo=>\n   \n")
	want := []FilterRule{
		{Find: "Cline", Replace: ""},
		{Find: "Roo Code", Replace: "MyAgent"},
		{Find: "Foo", Replace: ""},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("解析结果不对：%+v", rules)
	}
	if got := ParseFilterRules(""); got != nil {
		t.Fatalf("空文本应返回 nil，实际 %+v", got)
	}
}

// 替换：systemInstruction 的 parts[].text 命中后换成替换值，不动用户消息。
func TestClientFilterRewriteParts(t *testing.T) {
	f := NewClientFilter(FilterRewrite, nil, true)
	body := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []any{map[string]any{"text": "You are Cline, a coding assistant."}},
		},
		"contents": []any{map[string]any{"parts": []any{map[string]any{"text": "Cline should stay here"}}}},
	}
	hits := f.Apply(body)
	if hits != 1 {
		t.Fatalf("应命中 1 次，实际 %d", hits)
	}
	si := body["systemInstruction"].(map[string]any)
	got := si["parts"].([]any)[0].(map[string]any)["text"]
	if got != "You are Antigravity, a coding assistant." {
		t.Fatalf("替换结果不对：%q", got)
	}
	// 用户消息不受影响。
	user := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]
	if user != "Cline should stay here" {
		t.Fatalf("不应改动 contents：%q", user)
	}
}

// 长词优先：Codex CLI、GitHub Copilot CLI 整体替换，不会只换掉前缀留下 " CLI"。
func TestClientFilterLongestFirst(t *testing.T) {
	f := NewClientFilter(FilterRewrite, nil, true)
	for _, tc := range []struct{ in, want string }{
		{"Powered by Codex CLI today", "Powered by Antigravity today"},
		{"Use GitHub Copilot CLI now", "Use Antigravity now"},
		{"plain Codex here", "plain Antigravity here"},
	} {
		body := map[string]any{"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": tc.in}}}}
		f.Apply(body)
		got := body["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]
		if got != tc.want {
			t.Fatalf("输入 %q：期望 %q，实际 %q", tc.in, tc.want, got)
		}
	}
}

// 自定义规则覆盖同名预置（不区分大小写），无 => 表示删除。
func TestClientFilterCustomOverridesAndDelete(t *testing.T) {
	custom := ParseFilterRules("cline => Robot\nAider") // 覆盖 Cline，删除 Aider
	f := NewClientFilter(FilterRewrite, custom, true)
	body := map[string]any{"systemInstruction": map[string]any{"parts": []any{
		map[string]any{"text": "CLINE and Aider and Cursor"},
	}}}
	f.Apply(body)
	got := body["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]
	// CLINE→Robot（不区分大小写），Aider→删除，Cursor→预置的 Antigravity。
	if got != "Robot and  and Antigravity" {
		t.Fatalf("自定义覆盖/删除结果不对：%q", got)
	}
}

// systemInstruction 直接是字符串、以及 snake_case 键也要处理。
func TestClientFilterStringAndSnakeKey(t *testing.T) {
	f := NewClientFilter(FilterRewrite, nil, true)
	body := map[string]any{"system_instruction": "I am Cursor."}
	if hits := f.Apply(body); hits != 1 {
		t.Fatalf("应命中 1 次，实际 %d", hits)
	}
	si := body["system_instruction"].(map[string]any)
	got := si["parts"].([]any)[0].(map[string]any)["text"]
	if got != "I am Antigravity." {
		t.Fatalf("字符串 systemInstruction 替换不对：%q", got)
	}
}

// Scan 只计数不改 body（拦截模式用）。
func TestClientFilterScan(t *testing.T) {
	f := NewClientFilter(FilterBlock, nil, true)
	body := map[string]any{"systemInstruction": map[string]any{"parts": []any{
		map[string]any{"text": "Cline and Cursor"},
	}}}
	if n := f.Scan(body); n != 2 {
		t.Fatalf("应命中 2 次，实际 %d", n)
	}
	// 不修改。
	got := body["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]
	if got != "Cline and Cursor" {
		t.Fatalf("Scan 不应改动 body：%q", got)
	}
}

// 空过滤器（无规则、nil）不应过滤或 panic。
func TestClientFilterEmpty(t *testing.T) {
	var nilFilter *ClientFilter
	if !nilFilter.Empty() {
		t.Fatal("nil 过滤器应为空")
	}
	body := map[string]any{"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "Cline"}}}}
	if n := nilFilter.Scan(body); n != 0 {
		t.Fatalf("nil 过滤器不应命中，实际 %d", n)
	}
	// 既不启用预置也没有自定义规则。
	f := NewClientFilter(FilterRewrite, nil, false)
	if !f.Empty() {
		t.Fatal("无规则时应为空")
	}
	if n := f.Apply(body); n != 0 {
		t.Fatalf("空过滤器不应替换，实际 %d", n)
	}
}
