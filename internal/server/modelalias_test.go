package server

import (
	"strings"
	"testing"
)

func TestNormalizeModelName(t *testing.T) {
	for in, want := range map[string]string{
		"claude-sonnet-5-5":            "claude-sonnet-5-5",
		"claude-sonnet-5-5-20261015":   "claude-sonnet-5-5",
		"claude-opus-5-5[1m]":          "claude-opus-5-5",
		"Claude-Opus-5-5-20261015[1m]": "claude-opus-5-5",
		" gemini-2.5-pro ":             "gemini-2.5-pro",
		"20261015":                     "20261015", // 整个名字都是日期时不动
		"claude-3-5-haiku-20241022":    "claude-3-5-haiku",
		"gemini-3.8-flash-high":        "gemini-3.8-flash-high",
		"model-1234567":                "model-1234567", // 7 位数字不是日期
	} {
		if got := normalizeModelName(in); got != want {
			t.Errorf("normalizeModelName(%q) = %q，应为 %q", in, got, want)
		}
	}
}

func TestResolveModelAlias(t *testing.T) {
	catalog := map[string]bool{"claude-sonnet-5-5": true, "claude-opus-5-5": true, "gemini-3-flash": true}
	known := func(m string) bool { return catalog[m] }
	custom, err := parseAliasRules("# 注释\n\nclaude-*haiku* => gemini-3-flash\ngemini-2.5-pro=>gemini-4-neon")
	if err != nil {
		t.Fatal(err)
	}
	rules := append(custom, presetAliases...)
	cases := []struct{ in, target, rule string }{
		{"claude-sonnet-5-5", "claude-sonnet-5-5", ""},                // 目录里有，原样
		{"claude-sonnet-5-5-20261015", "claude-sonnet-5-5", ""},       // 去掉日期后有
		{"claude-opus-5-5[1m]", "claude-opus-5-5", ""},                // 去掉 [1m] 后有
		{"claude-haiku-5-5", "gemini-3-flash", "claude-*haiku*"},      // 自定义规则优先于预置
		{"claude-sonnet-4-5", "claude-sonnet-5-5", "claude-*sonnet*"}, // 旧名走预置规则
		{"claude-3-7-sonnet-20250219", "claude-sonnet-5-5", "claude-*sonnet*"},
		{"Gemini-2.5-Pro", "gemini-4-neon", "gemini-2.5-pro"}, // 大小写不敏感
		{"gpt-5", "gpt-5", ""},                                // 都不匹配：交给执行层报不存在
	}
	for _, c := range cases {
		res := resolveModelAlias(c.in, rules, known)
		rule := ""
		if res.Rule != nil {
			rule = res.Rule.Pattern
		}
		if res.Target != c.target || rule != c.rule {
			t.Errorf("%s → %s（规则 %q），应为 %s（规则 %q）", c.in, res.Target, rule, c.target, c.rule)
		}
	}
	// 只用预置规则时 Haiku 映射到 Sonnet 最低档。
	if res := resolveModelAlias("claude-haiku-5-5", presetAliases, known); res.Target != "claude-sonnet-5-5-low" {
		t.Errorf("预置 Haiku → %s", res.Target)
	}
}

func TestParseAliasRulesErrors(t *testing.T) {
	for text, want := range map[string]string{
		"a => b\nno arrow":   "第 2 行",
		"=> target":          "第 1 行",
		"pattern =>":         "第 1 行",
		"a => claude-*":      "通配符",
		"a => claude sonnet": "空格",
	} {
		if _, err := parseAliasRules(text); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v，应包含 %q", text, err, want)
		}
	}
}
