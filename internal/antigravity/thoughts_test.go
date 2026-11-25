package antigravity

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripThoughtParts(t *testing.T) {
	in := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"思考过程","thought":true},{"text":"答案"}]},"finishReason":"STOP"}],"usageMetadata":{"thoughtsTokenCount":42,"totalTokenCount":50}}`)
	out := stripThoughtParts(in)
	parts := gjson.GetBytes(out, "candidates.0.content.parts")
	if parts.Get("#").Int() != 1 {
		t.Fatalf("应只剩 1 个 part，实际 %s", parts.Raw)
	}
	if parts.Get("0.text").String() != "答案" || parts.Get("0.thought").Exists() {
		t.Fatalf("应只保留非思考 part：%s", parts.Raw)
	}
	// usageMetadata（含 thoughtsTokenCount）不动，finishReason 保留。
	if gjson.GetBytes(out, "usageMetadata.thoughtsTokenCount").Int() != 42 {
		t.Fatal("usageMetadata 应保留")
	}
	if gjson.GetBytes(out, "candidates.0.finishReason").String() != "STOP" {
		t.Fatal("finishReason 应保留")
	}
}

// 没有思考 part 时原样返回（字节不变，保持透传）。
func TestStripThoughtPartsNoThought(t *testing.T) {
	in := []byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`)
	out := stripThoughtParts(in)
	if !bytes.Equal(in, out) {
		t.Fatalf("没有思考 part 时应原样返回，实际 %s", out)
	}
}

// 思考 part 占满一帧时去空，不 panic。
func TestStripThoughtPartsOnlyThought(t *testing.T) {
	in := []byte(`{"candidates":[{"content":{"parts":[{"text":"t","thought":true}]}}]}`)
	out := stripThoughtParts(in)
	if gjson.GetBytes(out, "candidates.0.content.parts.#").Int() != 0 {
		t.Fatalf("应去空 parts，实际 %s", out)
	}
}
