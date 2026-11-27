package server

import (
	"testing"

	"github.com/shiceai/agy2api/internal/antigravity"
)

// 模板为空时回退到内置官方基线，非空时用自定义。
func TestSystemPromptTemplateFallback(t *testing.T) {
	if got := (systemPromptView{Template: ""}).template(); got != antigravity.BuiltinSystemPrompt {
		t.Fatalf("空模板应回退到内置基线，实际 %q", got)
	}
	if got := (systemPromptView{Template: "  "}).template(); got != antigravity.BuiltinSystemPrompt {
		t.Fatalf("全空白模板应回退到内置基线，实际 %q", got)
	}
	if got := (systemPromptView{Template: "custom"}).template(); got != "custom" {
		t.Fatalf("自定义模板应原样用，实际 %q", got)
	}
	if antigravity.BuiltinSystemPrompt == "" {
		t.Fatal("内置基线不应为空")
	}
}
