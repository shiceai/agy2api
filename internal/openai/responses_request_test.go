package openai

import (
	"errors"
	"strings"
	"testing"
)

func mustParseResponses(t *testing.T, body string) *ResponsesRequest {
	t.Helper()
	req, err := ParseResponsesRequest([]byte(body))
	if err != nil {
		t.Fatalf("ParseResponsesRequest: %v", err)
	}
	return req
}

// Codex 实抓的形态：系统提示在开头的 developer 消息里，工具有函数、命名空间和内置工具，带 reasoning、include 和
// prompt_cache_key；历史里有 reasoning（只带签名）、命名空间工具调用和结果，对话中间还有 developer 消息。
func TestParseResponsesCodexShape(t *testing.T) {
	req := mustParseResponses(t, `{
		"model": "gemini-3-flash", "stream": true, "store": false, "parallel_tool_calls": true,
		"instructions": "Base instructions.",
		"include": ["reasoning.encrypted_content"], "prompt_cache_key": " sess-1 ", "reasoning": {"effort": "high", "summary": "auto"},
		"client_metadata": {"session_id": "sess-1"},
		"input": [
			{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "You are a coding agent."}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "<environment_context/>"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "spawn a helper"}]},
			{"type": "reasoning", "id": "rs_1", "summary": [], "content": null, "encrypted_content": "SIG_A"},
			{"type": "function_call", "id": "fc_1", "call_id": "call_1", "namespace": "multi_agent_v1", "name": "spawn_agent", "arguments": "{\"task\":\"x\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "agent-7"},
			{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "Sandbox changed."}]},
			{"type": "web_search_call", "id": "ws_1"}
		],
		"tools": [
			{"type": "function", "name": "exec_command", "description": "Run a command.", "strict": false,
			 "parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}, "required": ["cmd"]}},
			{"type": "namespace", "name": "multi_agent_v1", "description": "Sub-agents.", "tools": [
				{"type": "function", "name": "spawn_agent", "description": "Spawn.", "parameters": {"type": "object", "properties": {"task": {"type": "string"}}}},
				{"type": "custom", "name": "send_patch", "format": {"type": "text"}}
			]},
			{"type": "web_search", "external_web_access": false}
		],
		"tool_choice": "auto"
	}`)
	if req.Model != "gemini-3-flash" || !req.Stream || req.PromptCacheKey != "sess-1" || !req.EncryptedReasoning {
		t.Fatalf("model/stream/cache key/encrypted = %q %v %q %v", req.Model, req.Stream, req.PromptCacheKey, req.EncryptedReasoning)
	}
	if got := asJSON(t, req.Body["systemInstruction"]); got != `{"parts":[{"text":"Base instructions."},{"text":"You are a coding agent."}],"role":"user"}` {
		t.Fatalf("systemInstruction = %s", got)
	}
	want := `[{"parts":[{"text":"<environment_context/>"},{"text":"spawn a helper"}],"role":"user"},` +
		`{"parts":[{"functionCall":{"args":{"task":"x"},"id":"call_1","name":"multi_agent_v1__spawn_agent"},"thoughtSignature":"SIG_A"}],"role":"model"},` +
		`{"parts":[{"functionResponse":{"id":"call_1","name":"multi_agent_v1__spawn_agent","response":{"result":"agent-7"}}},` +
		`{"text":"<system-reminder>\nSandbox changed.\n</system-reminder>"}],"role":"user"}]`
	if got := asJSON(t, req.Body["contents"]); got != want {
		t.Fatalf("contents = %s", got)
	}
	wantTools := `[{"functionDeclarations":[` +
		`{"description":"Run a command.","name":"exec_command","parameters":{"properties":{"cmd":{"type":"string"}},"required":["cmd"],"type":"object"}},` +
		`{"description":"Spawn.","name":"multi_agent_v1__spawn_agent","parameters":{"properties":{"task":{"type":"string"}},"type":"object"}},` +
		`{"name":"multi_agent_v1__send_patch","parameters":{"properties":{"input":{"description":"The raw input text for this tool.","type":"string"}},"required":["input"],"type":"object"}}]}]`
	if got := asJSON(t, req.Body["tools"]); got != wantTools {
		t.Fatalf("tools = %s", got)
	}
	if got := asJSON(t, req.Body["toolConfig"]); got != `{"functionCallingConfig":{"mode":"AUTO"}}` {
		t.Fatalf("toolConfig = %s", got)
	}
	if got := asJSON(t, req.Tools); got != `{"multi_agent_v1__send_patch":{"Namespace":"multi_agent_v1","Name":"send_patch","Custom":true},`+
		`"multi_agent_v1__spawn_agent":{"Namespace":"multi_agent_v1","Name":"spawn_agent","Custom":false}}` {
		t.Fatalf("Tools = %s", got)
	}
	if got := asJSON(t, req.Body["generationConfig"]); got != `{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"high"}}` {
		t.Fatalf("generationConfig = %s", got)
	}
	if strings.Join(req.Dropped, ",") != "web_search_call item,tool type web_search" {
		t.Fatalf("dropped = %v", req.Dropped)
	}
	// 回显请求参数，没给的用默认值。
	if req.Echo["instructions"] != "Base instructions." || req.Echo["tool_choice"] != "auto" || req.Echo["max_output_tokens"] != nil ||
		asJSON(t, req.Echo["reasoning"]) != `{"effort":"high","summary":"auto"}` || asJSON(t, req.Echo["text"]) != `{"format":{"type":"text"}}` {
		t.Fatalf("echo = %s", asJSON(t, req.Echo))
	}
}

// reasoning 条目：带摘要和签名的 → 带签名的思考 part（Claude）；只带签名的 → 挂到接下来的工具调用上，
// 用户消息开始时作废；不带签名的去掉。
func TestParseResponsesReasoning(t *testing.T) {
	req := mustParseResponses(t, `{"model": "claude-sonnet-5-5", "input": [
		{"role": "user", "content": "q1"},
		{"type": "reasoning", "summary": [{"type": "summary_text", "text": "think A"}, {"type": "summary_text", "text": "think B"}], "encrypted_content": "SIG_C"},
		{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "calling"}]},
		{"type": "function_call", "call_id": "toolu_1", "name": "read", "arguments": "{}"},
		{"type": "function_call_output", "call_id": "toolu_1", "output": [{"type": "input_text", "text": "line1"}, {"type": "input_image", "image_url": "data:image/png;base64,AAAA"}]},
		{"type": "reasoning", "summary": [], "encrypted_content": "SIG_UNUSED"},
		{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "done"}]},
		{"role": "user", "content": "q2"},
		{"type": "function_call", "call_id": "call_2", "name": "read", "arguments": "not json"},
		{"type": "reasoning", "summary": [{"type": "summary_text", "text": "unsigned"}]}
	]}`)
	want := `[{"parts":[{"text":"q1"}],"role":"user"},` +
		`{"parts":[{"text":"think A\n\nthink B","thought":true,"thoughtSignature":"SIG_C"},{"text":"calling"},{"functionCall":{"args":{},"id":"toolu_1","name":"read"}}],"role":"model"},` +
		`{"parts":[{"functionResponse":{"id":"toolu_1","name":"read","parts":[{"inlineData":{"data":"AAAA","mimeType":"image/png"}}],"response":{"result":"line1"}}}],"role":"user"},` +
		`{"parts":[{"text":"done"}],"role":"model"},` +
		`{"parts":[{"text":"q2"}],"role":"user"},` +
		`{"parts":[{"functionCall":{"args":{},"id":"call_2","name":"read"}}],"role":"model"}]`
	if got := asJSON(t, req.Body["contents"]); got != want {
		t.Fatalf("contents = %s", got)
	}
	if strings.Join(req.Dropped, ",") != "tool call arguments that are not a JSON object,reasoning item without encrypted_content" {
		t.Fatalf("dropped = %v", req.Dropped)
	}
	if req.EncryptedReasoning {
		t.Fatal("没要 reasoning.encrypted_content 时不应回签名")
	}
}

// 自定义工具：声明转成带 input 参数的函数（语法写进说明），调用的输入文本放进 args.input；tool_choice 指定自定义工具。
func TestParseResponsesCustomTool(t *testing.T) {
	req := mustParseResponses(t, `{"model": "m", "input": [
		{"role": "user", "content": [{"type": "input_text", "text": "patch it"}]},
		{"type": "custom_tool_call", "call_id": "call_9", "name": "apply_patch", "input": "*** Begin Patch\n*** End Patch"},
		{"type": "custom_tool_call_output", "call_id": "call_9", "output": "ok"}
	], "tools": [{"type": "custom", "name": "apply_patch", "description": "Edit files.",
		"format": {"type": "grammar", "syntax": "lark", "definition": "start: \"x\""}}],
		"tool_choice": {"type": "custom", "name": "apply_patch"},
		"max_output_tokens": 512, "temperature": 0.2, "top_p": 0.9,
		"text": {"format": {"type": "json_schema", "name": "out", "schema": {"type": "object"}}}}`)
	want := `[{"parts":[{"text":"patch it"}],"role":"user"},` +
		`{"parts":[{"functionCall":{"args":{"input":"*** Begin Patch\n*** End Patch"},"id":"call_9","name":"apply_patch"}}],"role":"model"},` +
		`{"parts":[{"functionResponse":{"id":"call_9","name":"apply_patch","response":{"result":"ok"}}}],"role":"user"}]`
	if got := asJSON(t, req.Body["contents"]); got != want {
		t.Fatalf("contents = %s", got)
	}
	decl := asJSON(t, req.Body["tools"])
	if !strings.Contains(decl, `"description":"Edit files.\n\nThe input must follow this lark grammar:\nstart: \"x\""`) {
		t.Fatalf("tools = %s", decl)
	}
	if got := asJSON(t, req.Body["toolConfig"]); got != `{"functionCallingConfig":{"allowedFunctionNames":["apply_patch"],"mode":"ANY"}}` {
		t.Fatalf("toolConfig = %s", got)
	}
	if got := asJSON(t, req.Body["generationConfig"]); got != `{"maxOutputTokens":512,"responseJsonSchema":{"type":"object"},"responseMimeType":"application/json","temperature":0.2,"topP":0.9}` {
		t.Fatalf("generationConfig = %s", got)
	}
}

func TestParseResponsesStringInput(t *testing.T) {
	req := mustParseResponses(t, `{"model": "m", "input": "hello"}`)
	if got := asJSON(t, req.Body); got != `{"contents":[{"parts":[{"text":"hello"}],"role":"user"}]}` {
		t.Fatalf("body = %s", got)
	}
}

func TestParseResponsesErrors(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`not json`, "invalid JSON body"},
		{`{"input": "x"}`, "model: field required"},
		{`{"model": "m", "input": "x", "previous_response_id": "resp_1"}`, "previous_response_id: stored responses are not supported"},
		{`{"model": "m", "input": "x", "conversation": "conv_1"}`, "conversation: stored conversations are not supported"},
		{`{"model": "m", "input": "x", "background": true}`, "background: background responses are not supported"},
		{`{"model": "m", "input": ""}`, "input: must not be empty"},
		{`{"model": "m", "input": [{"type": "item_reference", "id": "msg_1"}]}`, "input.0: item_reference needs stored items"},
		{`{"model": "m", "input": [{"type": "web_search_call"}]}`, "input: no content to send"},
		{`{"model": "m", "input": [{"role": "tool", "content": "x"}]}`, "input.0.role: must be system, developer, user or assistant"},
		{`{"model": "m", "input": [{"type": "function_call_output", "output": "x"}]}`, "input.0.call_id: field required"},
		{`{"model": "m", "input": [{"role": "user", "content": [{"type": "input_image", "file_id": "file_1"}]}]}`, "input.0.content.0.file_id: uploaded files"},
		{`{"model": "m", "input": "x", "instructions": ["a"]}`, "instructions: must be a string"},
		{`{"model": "m", "input": "x", "reasoning": {"effort": "max"}}`, "reasoning.effort: must be one of"},
		{`{"model": "m", "input": "x", "text": {"format": {"type": "xml"}}}`, "text.format.type: must be text, json_object or json_schema"},
		{`{"model": "m", "input": "x", "max_output_tokens": 0}`, "max_output_tokens: must be at least 1"},
		{`{"model": "m", "input": "x", "tools": [{"type": "function"}]}`, "tools.0.name: field required"},
	} {
		_, err := ParseResponsesRequest([]byte(tc.body))
		var inv *InvalidError
		if !errors.As(err, &inv) || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%s: err = %v，应以 %q 开头", tc.body, err, tc.want)
		}
	}
}
