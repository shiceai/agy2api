package antigravity

import (
	"encoding/json"
	"testing"
)

// TestSanitizeToolsFlattensAndRenames 验证工具参数 schema 被清洗为 parametersJsonSchema，
// 并完成 $ref 内联、anyOf 展平、去掉不支持关键字。
func TestSanitizeToolsFlattensAndRenames(t *testing.T) {
	inner := map[string]any{
		"tools": []any{
			map[string]any{
				"function_declarations": []any{
					map[string]any{
						"name": "lookup",
						"parameters": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"$defs": map[string]any{
								"Loc": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
							},
							"properties": map[string]any{
								"where": map[string]any{"$ref": "#/$defs/Loc"},
								"count": map[string]any{"anyOf": []any{
									map[string]any{"type": "integer"},
									map[string]any{"type": "null"},
								}},
							},
							"required": []any{"where"},
						},
					},
				},
			},
		},
	}

	sanitizeTools(inner)

	raw, _ := json.Marshal(inner)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)

	tool := got["tools"].([]any)[0].(map[string]any)
	if _, ok := tool["function_declarations"]; ok {
		t.Fatalf("function_declarations should be renamed to functionDeclarations")
	}
	decls := tool["functionDeclarations"].([]any)
	decl := decls[0].(map[string]any)
	if _, ok := decl["parameters"]; ok {
		t.Fatalf("parameters should be moved to parametersJsonSchema")
	}
	schema := decl["parametersJsonSchema"].(map[string]any)
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("additionalProperties should be stripped")
	}
	if _, ok := schema["$defs"]; ok {
		t.Fatalf("$defs should be stripped from the cleaned schema")
	}
	props := schema["properties"].(map[string]any)
	// $ref 应被内联成对象。
	where := props["where"].(map[string]any)
	if where["type"] != "object" {
		t.Fatalf("where should be inlined to an object, got %v", where)
	}
	// anyOf[int,null] 应展平为 integer + nullable。
	count := props["count"].(map[string]any)
	if count["type"] != "integer" {
		t.Fatalf("count should flatten to integer, got %v", count)
	}
	if count["nullable"] != true {
		t.Fatalf("count should be marked nullable, got %v", count)
	}
}

// TestStubToolThoughtSignatures 验证出站 functionCall 补占位签名，入站 functionResponse 去签名。
func TestStubToolThoughtSignatures(t *testing.T) {
	inner := map[string]any{
		"contents": []any{
			map[string]any{"role": "model", "parts": []any{
				map[string]any{"functionCall": map[string]any{"name": "f", "args": map[string]any{}}},
			}},
			map[string]any{"role": "user", "parts": []any{
				map[string]any{"functionResponse": map[string]any{"name": "f"}, "thoughtSignature": "stale"},
			}},
		},
	}
	stubToolThoughtSignatures(inner)

	contents := inner["contents"].([]any)
	callPart := contents[0].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if callPart["thoughtSignature"] != skipThoughtSignature {
		t.Fatalf("functionCall should get stub signature, got %v", callPart["thoughtSignature"])
	}
	respPart := contents[1].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if _, ok := respPart["thoughtSignature"]; ok {
		t.Fatalf("functionResponse stale signature should be stripped")
	}
}
