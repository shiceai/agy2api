package antigravity

import (
	"encoding/json"
	"testing"
)

func TestRequestedTier(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"没有思考参数", `{"contents":[]}`, ""},
		{"thinkingLevel", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}`, "high"},
		{"MINIMAL 视作最低档", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"minimal"}}}`, "low"},
		{"level 优先于 budget", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low","thinkingBudget":20000}}}`, "low"},
		{"不认识的 level 退回 budget", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"turbo","thinkingBudget":4000}}}`, "medium"},
		{"budget 0", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`, "low"},
		{"budget 1024", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}}`, "low"},
		{"budget 8192", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`, "medium"},
		{"budget 24576", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":24576}}}`, "high"},
		{"budget -1 动态", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`, "high"},
		{"snake_case", `{"generation_config":{"thinking_config":{"thinking_level":"xhigh"}}}`, "xhigh"},
		{"只有 includeThoughts", `{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`, ""},
	}
	for _, tc := range cases {
		var body map[string]any
		if err := json.Unmarshal([]byte(tc.body), &body); err != nil {
			t.Fatal(err)
		}
		if got := RequestedTier(body); got != tc.want {
			t.Errorf("%s：期望 %q，实际 %q", tc.name, tc.want, got)
		}
	}
}

func TestResolveModel(t *testing.T) {
	catalog := map[string]bool{
		"gemini-2.5-flash":              true,
		"gemini-3.8-flash-low":          true,
		"gemini-3.8-flash-medium":       true,
		"gemini-3.8-flash-high":         true,
		"gemini-3.8-flash-tiered":       true,
		"gemini-4-neon-medium":          true,
		"gemini-4-neon-xhigh":           true,
		"gemini-4-hydrogen-tiered-high": true,
		"gemini-4-carbon-disabled":      true,
	}
	has := func(id string) bool { return catalog[id] }
	cases := []struct {
		requested, tier, want string
		ok                    bool
	}{
		{"gemini-2.5-flash", "high", "gemini-2.5-flash", true},
		{"gemini-3.8-flash-low", "high", "gemini-3.8-flash-low", true}, // 精确 ID 不受档位影响
		{"gemini-3.8-flash", "low", "gemini-3.8-flash-low", true},
		{"gemini-3.8-flash", "medium", "gemini-3.8-flash-medium", true},
		{"gemini-3.8-flash", "xhigh", "gemini-3.8-flash-high", true}, // 没有该档先往低找
		{"gemini-4-neon", "low", "gemini-4-neon-medium", true},       // 低处没有再往高找
		{"gemini-4-neon", "high", "gemini-4-neon-medium", true},
		{"gemini-4-neon", "xhigh", "gemini-4-neon-xhigh", true},
		{"gemini-4-neon", "bogus", "gemini-4-neon-medium", true}, // 不认识的档位按默认档
		{"gemini-4-hydrogen", "high", "", false},                 // tiered-high 不是 <家族>-<档位>
		{"gemini-4-carbon", "medium", "", false},
		{"gemini-9", "medium", "", false},
	}
	for _, tc := range cases {
		got, ok := ResolveModel(has, tc.requested, tc.tier)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ResolveModel(%q, %q) = %q %v，期望 %q %v", tc.requested, tc.tier, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIsTierModel(t *testing.T) {
	for id, want := range map[string]bool{
		"gemini-3.8-flash-high":         true,
		"claude-opus-5-5-low":           true,
		"gemini-4-neon-xhigh":           true,
		"gemini-4-hydrogen-tiered-high": true,
		"gemini-2.5-flash":              false,
		"gemini-3.8-flash-tiered":       false,
		"gemini-3-flash":                false,
	} {
		if got := IsTierModel(id); got != want {
			t.Errorf("IsTierModel(%q) = %v，期望 %v", id, got, want)
		}
	}
}

// 分档模型去掉客户端的思考强度，保留 includeThoughts；非分档模型原样保留。
func TestBuildEnvelopeStripsTierThinking(t *testing.T) {
	budget := 8192
	cases := []struct {
		name  string
		model string
		info  *ModelInfo
		body  string
		want  string // request.generationConfig 的 JSON，空串表示不应有该字段
	}{
		{"分档模型去掉 level 和 budget", "gemini-3.8-flash-high", nil,
			`{"generationConfig":{"temperature":1,"thinkingConfig":{"thinkingLevel":"LOW","thinkingBudget":100,"includeThoughts":true}}}`,
			`{"temperature":1,"thinkingConfig":{"includeThoughts":true}}`},
		{"只剩空 thinkingConfig 时整个删掉", "claude-opus-5-5-high", nil,
			`{"generation_config":{"thinking_config":{"thinking_level":"high"}}}`,
			`{}`},
		{"空了之后用目录默认值", "gpt-oss-120b-medium", &ModelInfo{SupportsThinking: true, ThinkingBudget: &budget},
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":100}}}`,
			`{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":8192}}`},
		{"非分档模型原样保留", "gemini-3-flash", nil,
			`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"LOW"}}}`,
			`{"thinkingConfig":{"thinkingLevel":"LOW"}}`},
	}
	for _, tc := range cases {
		var body map[string]any
		if err := json.Unmarshal([]byte(tc.body), &body); err != nil {
			t.Fatal(err)
		}
		raw, err := BuildEnvelope(BuildParams{ProjectID: "p", WireModel: tc.model, GeminiRequest: body, Model: tc.info})
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			Model   string `json:"model"`
			Request struct {
				GenerationConfig json.RawMessage `json:"generationConfig"`
				SnakeConfig      json.RawMessage `json:"generation_config"`
			} `json:"request"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		got := string(env.Request.GenerationConfig)
		if got == "" {
			got = string(env.Request.SnakeConfig)
		}
		if env.Model != tc.model || got != tc.want {
			t.Errorf("%s：model=%s generationConfig=%s，期望 %s", tc.name, env.Model, got, tc.want)
		}
	}
}

func TestTierFamilies(t *testing.T) {
	got := TierFamilies([]string{
		"gemini-3.8-flash-high", "gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash-tiered",
		"gemini-4-neon-xhigh", "gemini-4-neon-high", "gemini-2.5-flash", "claude-opus-5-5-low",
	})
	want := map[string][]string{
		"gemini-3.8-flash": {"low", "medium", "high"},
		"gemini-4-neon":    {"high", "xhigh"},
		"claude-opus-5-5":  {"low"},
	}
	if len(got) != len(want) {
		t.Fatalf("家族应为 %v，实际 %v", want, got)
	}
	for family, tiers := range want {
		if len(got[family]) != len(tiers) {
			t.Fatalf("%s 的档位应为 %v，实际 %v", family, tiers, got[family])
		}
		for i := range tiers {
			if got[family][i] != tiers[i] {
				t.Fatalf("%s 的档位应为 %v（按强度排序），实际 %v", family, tiers, got[family])
			}
		}
	}
}

func TestReasoningLabel(t *testing.T) {
	cases := []struct {
		model, body, want string
	}{
		{"gemini-3.8-flash-high", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"LOW"}}}`, "high"}, // 分档模型以模型名为准
		{"gpt-oss-120b-medium", `{}`, "medium"},
		{"gemini-3-flash", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"LOW"}}}`, "low"},
		{"gemini-3-flash", `{"generation_config":{"thinking_config":{"thinking_level":"minimal"}}}`, "minimal"},
		{"gemini-2.5-flash", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":512}}}`, "budget:512"},
		{"gemini-2.5-flash", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`, "budget:-1"},
		{"gemini-2.5-flash", `{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`, ""},
		{"gemini-2.5-flash", `{}`, ""},
	}
	for _, tc := range cases {
		var body map[string]any
		if err := json.Unmarshal([]byte(tc.body), &body); err != nil {
			t.Fatal(err)
		}
		if got := ReasoningLabel(tc.model, body); got != tc.want {
			t.Errorf("ReasoningLabel(%s, %s) = %q，期望 %q", tc.model, tc.body, got, tc.want)
		}
	}
}
