package antigravity

import (
	"regexp"
	"sort"
	"strings"
)

// 客户端名称过滤：第三方编码客户端（Cline、Roo Code、Claude Code 等）会把自己的名字写进 system 提示词，
// 发到伪装成官方 Antigravity 客户端的请求里可能成为识别信号。这里把这些名字替换成官方客户端名（rewrite），
// 或在命中时拦截请求（block）。只改 systemInstruction，不动用户消息。
//
// 没有证据表明过滤能减少风控，改写 system 还可能影响客户端行为，所以默认关闭，由管理端开启。

// defaultReplacement 是预置客户端名的替换值：伪装成官方 Antigravity 客户端。
const defaultReplacement = "Antigravity"

// maxFilterRules 是自定义规则条数上限。
const maxFilterRules = 200

// ruleSeparator 分隔自定义规则的查找词和替换词。
const ruleSeparator = "=>"

// DefaultClientKeywords 是预置的第三方编码客户端名称，启用预置规则时各替换为 defaultReplacement。
// 只收真实存在的客户端。长词优先匹配，所以列表顺序不影响结果。
var DefaultClientKeywords = []string{
	"Claude Code", "Claude Agent SDK",
	"OpenAI Codex", "Codex CLI", "Codex",
	"GitHub Copilot CLI", "GitHub Copilot", "Copilot CLI",
	"Gemini Code Assist", "Gemini CLI",
	"Cursor", "Windsurf", "Codeium",
	"Cline", "Roo Code", "Kilo Code", "Aider", "Continue.dev",
	"Amazon Q Developer", "CodeWhisperer",
	"JetBrains AI Assistant", "JetBrains Junie", "Junie",
	"Kiro", "Qwen Code", "Trae", "Tabnine",
	"Sourcegraph Cody", "Augment Code", "Replit Agent",
	"Zed AI", "OpenHands", "Goose", "Qoder", "GitLab Duo",
	"Tabby", "Refact.ai", "Void Editor", "PearAI",
	"CodeBuddy", "Blackbox AI", "Qodo",
}

// FilterMode 是过滤模式。
type FilterMode string

const (
	FilterRewrite FilterMode = "rewrite" // 替换命中的客户端名称
	FilterBlock   FilterMode = "block"   // 命中即拦截请求
)

// FilterRule 是一条替换规则：命中 Find（不区分大小写）时替换成 Replace，Replace 为空表示删除。
type FilterRule struct {
	Find    string
	Replace string
}

// ClientFilter 是编译好的客户端名称过滤器。nil 或无有效规则时不过滤（Empty 为真）。
type ClientFilter struct {
	mode FilterMode
	re   *regexp.Regexp    // 所有查找词的合并正则（不区分大小写），长词在前；无规则时为 nil
	repl map[string]string // 小写查找词 → 替换词
}

// ParseFilterRules 解析自定义规则文本：每行一条 `查找词=>替换词`，无 => 表示删除（替换为空）；
// 空行和 # 开头的注释忽略；两端空白去掉；查找词为空的行丢弃；最多 maxFilterRules 条。
func ParseFilterRules(text string) []FilterRule {
	var rules []FilterRule
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		find, replace := line, ""
		if i := strings.Index(line, ruleSeparator); i >= 0 {
			find = strings.TrimSpace(line[:i])
			replace = strings.TrimSpace(line[i+len(ruleSeparator):])
		}
		if find == "" {
			continue
		}
		rules = append(rules, FilterRule{Find: find, Replace: replace})
		if len(rules) >= maxFilterRules {
			break
		}
	}
	return rules
}

// NewClientFilter 按模式、自定义规则和是否启用预置规则编译过滤器。
// 自定义规则覆盖同名（不区分大小写）的预置规则；查找词按长度降序，保证长名先匹配（如 "Codex CLI" 先于 "Codex"）。
func NewClientFilter(mode FilterMode, custom []FilterRule, useDefaults bool) *ClientFilter {
	f := &ClientFilter{mode: mode}
	seen := map[string]int{}
	var rules []FilterRule
	add := func(find, replace string) {
		find = strings.TrimSpace(find)
		if find == "" {
			return
		}
		key := strings.ToLower(find)
		if i, ok := seen[key]; ok {
			rules[i] = FilterRule{Find: find, Replace: replace} // 后者覆盖前者
			return
		}
		seen[key] = len(rules)
		rules = append(rules, FilterRule{Find: find, Replace: replace})
	}
	if useDefaults {
		for _, k := range DefaultClientKeywords {
			add(k, defaultReplacement)
		}
	}
	for _, r := range custom {
		add(r.Find, r.Replace)
	}
	if len(rules) == 0 {
		return f
	}
	// 长词优先：Go 正则是 leftmost-first，交替项里排在前面的先匹配。
	sort.SliceStable(rules, func(i, j int) bool { return len(rules[i].Find) > len(rules[j].Find) })
	alts := make([]string, len(rules))
	f.repl = make(map[string]string, len(rules))
	for i, r := range rules {
		alts[i] = regexp.QuoteMeta(r.Find)
		f.repl[strings.ToLower(r.Find)] = r.Replace
	}
	f.re = regexp.MustCompile("(?i)" + strings.Join(alts, "|"))
	return f
}

// Mode 返回过滤模式。
func (f *ClientFilter) Mode() FilterMode { return f.mode }

// Empty 报告过滤器是否没有任何有效规则（此时不应过滤）。
func (f *ClientFilter) Empty() bool { return f == nil || f.re == nil }

// Apply 对请求体的 systemInstruction 应用替换，返回命中次数，原地修改（只在命中时重建 parts/instruction）。
func (f *ClientFilter) Apply(body map[string]any) int { return f.walk(body, true) }

// Scan 返回 systemInstruction 里命中的次数，不修改 body（拦截模式用）。
func (f *ClientFilter) Scan(body map[string]any) int { return f.walk(body, false) }

// walk 遍历 systemInstruction 的文本段。mutate 为真时替换，否则只计数。
// systemInstruction 可能写成 camelCase 或 snake_case，值可能是字符串或带 parts 的对象。
func (f *ClientFilter) walk(body map[string]any, mutate bool) int {
	if f.Empty() || body == nil {
		return 0
	}
	key := "systemInstruction"
	inst, ok := body[key]
	if !ok {
		key = "system_instruction"
		inst, ok = body[key]
	}
	if !ok {
		return 0
	}
	switch v := inst.(type) {
	case string:
		out, hits := f.replace(v)
		if hits > 0 && mutate {
			body[key] = map[string]any{"parts": []any{map[string]any{"text": out}}}
		}
		return hits
	case map[string]any:
		return f.walkParts(v, mutate)
	default:
		return 0
	}
}

func (f *ClientFilter) walkParts(inst map[string]any, mutate bool) int {
	parts, ok := inst["parts"].([]any)
	if !ok {
		return 0
	}
	total := 0
	for i, p := range parts {
		switch pv := p.(type) {
		case string:
			out, hits := f.replace(pv)
			if hits > 0 {
				total += hits
				if mutate {
					parts[i] = map[string]any{"text": out}
				}
			}
		case map[string]any:
			total += f.replacePartText(pv, mutate)
		}
	}
	return total
}

// replacePartText 处理一个 part 的 text 字段：字符串直接替换，数组按元素（字符串或 {text}）替换。
func (f *ClientFilter) replacePartText(part map[string]any, mutate bool) int {
	switch t := part["text"].(type) {
	case string:
		out, hits := f.replace(t)
		if hits > 0 && mutate {
			part["text"] = out
		}
		return hits
	case []any:
		total := 0
		for i, el := range t {
			switch ev := el.(type) {
			case string:
				out, hits := f.replace(ev)
				if hits > 0 {
					total += hits
					if mutate {
						t[i] = out
					}
				}
			case map[string]any:
				if s, ok := ev["text"].(string); ok {
					out, hits := f.replace(s)
					if hits > 0 {
						total += hits
						if mutate {
							ev["text"] = out
						}
					}
				}
			}
		}
		return total
	default:
		return 0
	}
}

// replace 对一段文本应用规则，返回替换后的文本和命中次数。单次替换：替换结果不再被其它规则命中。
func (f *ClientFilter) replace(text string) (string, int) {
	if f.re == nil || text == "" {
		return text, 0
	}
	hits := 0
	out := f.re.ReplaceAllStringFunc(text, func(m string) string {
		hits++
		if r, ok := f.repl[strings.ToLower(m)]; ok {
			return r
		}
		return m
	})
	return out, hits
}
