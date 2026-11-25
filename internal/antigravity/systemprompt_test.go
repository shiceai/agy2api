package antigravity

import (
	"strings"
	"testing"
)

func clientBody() map[string]any {
	return map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "You are Cline."}}},
		"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
	}
}

func sysText(body map[string]any) []string {
	si, _ := body["systemInstruction"].(map[string]any)
	parts, _ := si["parts"].([]any)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if m, ok := p.(map[string]any); ok {
			if s, ok := m["text"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestApplySystemPromptPrepend(t *testing.T) {
	body := clientBody()
	if !ApplySystemPrompt(body, SystemPromptPrepend, "OFFICIAL") {
		t.Fatal("应返回已改动")
	}
	got := sysText(body)
	if len(got) != 2 || got[0] != "OFFICIAL" || got[1] != "You are Cline." {
		t.Fatalf("前置模式 system 不对：%v", got)
	}
	// contents 不变。
	if len(body["contents"].([]any)) != 1 {
		t.Fatalf("前置模式不应动 contents：%v", body["contents"])
	}
}

func TestApplySystemPromptReplace(t *testing.T) {
	body := clientBody()
	ApplySystemPrompt(body, SystemPromptReplace, "OFFICIAL")
	got := sysText(body)
	if len(got) != 1 || got[0] != "OFFICIAL" {
		t.Fatalf("替换模式应只剩模板：%v", got)
	}
	if len(body["contents"].([]any)) != 1 {
		t.Fatalf("替换模式不应动 contents：%v", body["contents"])
	}
}

func TestApplySystemPromptAsUser(t *testing.T) {
	body := clientBody()
	ApplySystemPrompt(body, SystemPromptAsUser, "OFFICIAL")
	// system 只剩模板。
	if got := sysText(body); len(got) != 1 || got[0] != "OFFICIAL" {
		t.Fatalf("客户端转用户：system 应只剩模板，实际 %v", got)
	}
	// contents 开头多一条 user 消息：桥接说明 + 原客户端 system。
	contents := body["contents"].([]any)
	if len(contents) != 2 {
		t.Fatalf("应在 contents 开头插一条，实际 %d 条", len(contents))
	}
	first := contents[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("插入的消息应是 user 角色：%v", first["role"])
	}
	parts := first["parts"].([]any)
	bridge := parts[0].(map[string]any)["text"].(string)
	moved := parts[1].(map[string]any)["text"].(string)
	if !strings.Contains(bridge, "client application") || moved != "You are Cline." {
		t.Fatalf("桥接或移动的客户端 system 不对：bridge=%q moved=%q", bridge, moved)
	}
	// 原来的 user 消息还在后面。
	if contents[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"] != "hi" {
		t.Fatalf("原 user 消息应保留在后面")
	}
}

// 没有客户端 system 时：各模式都只设模板，as_user 不插空的 user 消息。
func TestApplySystemPromptNoClientSystem(t *testing.T) {
	for _, mode := range []SystemPromptMode{SystemPromptPrepend, SystemPromptReplace, SystemPromptAsUser} {
		body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}}
		ApplySystemPrompt(body, mode, "OFFICIAL")
		if got := sysText(body); len(got) != 1 || got[0] != "OFFICIAL" {
			t.Fatalf("模式 %s：system 应只有模板，实际 %v", mode, got)
		}
		if n := len(body["contents"].([]any)); n != 1 {
			t.Fatalf("模式 %s：没有客户端 system 时不应插 user 消息，实际 %d 条", mode, n)
		}
	}
}

// snake_case 键、字符串形式的 systemInstruction 都要处理，并归一到 camelCase。
func TestApplySystemPromptSnakeAndString(t *testing.T) {
	body := map[string]any{"system_instruction": "You are Cursor."}
	ApplySystemPrompt(body, SystemPromptPrepend, "OFFICIAL")
	if _, ok := body["system_instruction"]; ok {
		t.Fatal("应删掉 snake_case 键")
	}
	got := sysText(body)
	if len(got) != 2 || got[0] != "OFFICIAL" || got[1] != "You are Cursor." {
		t.Fatalf("snake_case/字符串处理不对：%v", got)
	}
}

// 模板为空或 body 为空时不动。
func TestApplySystemPromptEmpty(t *testing.T) {
	body := clientBody()
	if ApplySystemPrompt(body, SystemPromptReplace, "   ") {
		t.Fatal("空模板不应改动")
	}
	if got := sysText(body); len(got) != 1 || got[0] != "You are Cline." {
		t.Fatalf("空模板时 system 应原样：%v", got)
	}
}

// hideThoughts 有 thinkingConfig 时把 includeThoughts 置 false、保留预算；没有时不动、不 panic。
func TestHideThoughts(t *testing.T) {
	inner := map[string]any{"generationConfig": map[string]any{
		"thinkingConfig": map[string]any{"includeThoughts": true, "thinkingBudget": 1024},
	}}
	hideThoughts(inner)
	tc := inner["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if tc["includeThoughts"] != false {
		t.Fatalf("includeThoughts 应为 false，实际 %v", tc["includeThoughts"])
	}
	if tc["thinkingBudget"] != 1024 {
		t.Fatalf("思考预算应保留，实际 %v", tc["thinkingBudget"])
	}
	// 没有 thinkingConfig 时不动、不 panic。
	hideThoughts(map[string]any{"generationConfig": map[string]any{}})
	hideThoughts(map[string]any{})
}
