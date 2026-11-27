package antigravity

import (
	"slices"
	"testing"
)

func turn(role string, parts ...any) map[string]any {
	return map[string]any{"role": role, "parts": parts}
}

func text(s string) map[string]any { return map[string]any{"text": s} }

// 上下文指纹：前缀性（追加消息后，原来的指纹原样出现在开头）；思考内容、思考签名、键顺序和网关转来的
// system 消息不影响；改动中间任一条，之后的指纹都变。
func TestContextFingerprints(t *testing.T) {
	base := map[string]any{"contents": []any{
		turn("user", text("设定")),
		turn("model", map[string]any{"text": "回复", "thoughtSignature": "sig-a"}),
		turn("user", text("继续")),
	}}
	fp := ContextFingerprints(base)
	if len(fp) != 3 || fp[0] == fp[1] || len(fp[2]) != 32 {
		t.Fatalf("每条消息一个指纹，32 位十六进制：%v", fp)
	}

	longer := map[string]any{"contents": append(slices.Clone(base["contents"].([]any)), turn("model", text("再回复")), turn("user", text("再继续")))}
	if got := ContextFingerprints(longer); !slices.Equal(got[:3], fp) || len(got) != 5 {
		t.Fatalf("追加消息后前缀指纹应不变：%v vs %v", got, fp)
	}

	// 思考 part、不同的思考签名、网关转来的 system 消息、额外的空消息都不影响指纹。
	variant := map[string]any{"contents": []any{
		turn("user", text(systemToUserBridge), text("客户端 system")),
		turn("user", text("设定")),
		turn("model", map[string]any{"text": "想了想", "thought": true}, map[string]any{"thoughtSignature": "sig-b", "text": "回复"}),
		turn("model"),
		turn("user", text("继续")),
	}}
	if got := ContextFingerprints(variant); !slices.Equal(got, fp) {
		t.Fatalf("思考内容、签名、转来的 system、空消息不应影响指纹：%v vs %v", got, fp)
	}

	edited := map[string]any{"contents": []any{
		turn("user", text("设定")),
		turn("model", text("改过的回复")),
		turn("user", text("继续")),
	}}
	got := ContextFingerprints(edited)
	if got[0] != fp[0] || got[1] == fp[1] || got[2] == fp[2] {
		t.Fatalf("改动第 2 条后，第 1 个指纹不变、之后的都变：%v vs %v", got, fp)
	}
	if len(ContextFingerprints(map[string]any{})) != 0 {
		t.Fatal("没有 contents 时应为空")
	}
}
