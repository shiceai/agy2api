package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

// 模型别名：客户端用的模型名（官方名、带日期的快照名等）不在号池目录里时，按规则换成号池里的模型。
// Gemini、Anthropic 两个入站接口都先过这一步，再按家族、档位解析（见 relay.LookupModel）。
//
// 解析顺序：原名在目录里（精确 ID 或家族名）就用原名；否则去掉末尾的 [...]（如 Claude Code 的 [1m]）和
// -YYYYMMDD 日期后再看一次；还不在就按规则（自定义在前、预置在后）找第一条匹配的，用它的目标；都不匹配时
// 用规范化后的名字（之后由执行层报模型不存在）。

// modelAliasView 是模型别名设置的读写形态。
type modelAliasView struct {
	// UsePresets 为真时在自定义规则之后加上预置规则。
	UsePresets bool `json:"use_presets"`
	// Rules 是自定义规则文本：每行一条 `别名 => 目标`，别名可以用 * 通配，# 开头是注释。
	Rules string `json:"rules"`
}

// aliasRule 是一条别名规则。Pattern 是小写的通配模式，Target 是目标模型（精确 ID 或家族名）。
type aliasRule struct {
	Pattern string `json:"pattern"`
	Target  string `json:"target"`
}

// presetAliases 是预置规则：Claude Code 等客户端发官方模型名，号池里的 Claude 是 claude-opus-5-5、
// claude-sonnet-5-5 两个家族；Haiku（后台小任务用）没有，用 Sonnet 的最低档。
var presetAliases = []aliasRule{
	{Pattern: "claude-*opus*", Target: "claude-opus-5-5"},
	{Pattern: "claude-*sonnet*", Target: "claude-sonnet-5-5"},
	{Pattern: "claude-*haiku*", Target: "claude-sonnet-5-5-low"},
}

// modelAliasSetting 存在 settings 的 model_aliases 键，默认启用预置规则、没有自定义规则。
var modelAliasSetting = jsonSetting[modelAliasView]{
	key:   "model_aliases",
	name:  "模型别名设置",
	limit: 1 << 16,
	defaults: func() modelAliasView {
		return modelAliasView{UsePresets: true}
	},
	validate: func(v modelAliasView) error {
		_, err := parseAliasRules(v.Rules)
		return err
	},
}

const aliasSeparator = "=>"

// parseAliasRules 解析自定义规则文本，格式不对时报出第几行。
func parseAliasRules(text string) ([]aliasRule, error) {
	var rules []aliasRule
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pattern, target, ok := strings.Cut(line, aliasSeparator)
		pattern, target = strings.ToLower(strings.TrimSpace(pattern)), strings.TrimSpace(target)
		if !ok || pattern == "" || target == "" {
			return nil, fmt.Errorf("第 %d 行格式不对，应为 别名 => 目标", i+1)
		}
		if strings.ContainsAny(target, "* \t") {
			return nil, fmt.Errorf("第 %d 行的目标不能带通配符或空格", i+1)
		}
		rules = append(rules, aliasRule{Pattern: pattern, Target: target})
	}
	return rules, nil
}

// rules 返回生效的规则：自定义在前、预置在后。
func (v modelAliasView) rules() []aliasRule {
	rules, _ := parseAliasRules(v.Rules) // 已校验过
	if v.UsePresets {
		rules = append(rules, presetAliases...)
	}
	return rules
}

// modelAliases 是内存里生效的规则，热路径只读它。
type modelAliases struct {
	mu    sync.RWMutex
	rules []aliasRule
}

func (a *modelAliases) set(rules []aliasRule) {
	a.mu.Lock()
	a.rules = rules
	a.mu.Unlock()
}

func (a *modelAliases) list() []aliasRule {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.rules
}

// modelSuffix 是模型名末尾可以去掉的部分：[...]（如 [1m]）和 -YYYYMMDD 日期。
var modelSuffix = regexp.MustCompile(`(\[[^\]]*\])$|(-\d{8})$`)

// normalizeModelName 去掉首尾空白、转小写，再去掉末尾的 [...] 和日期（可能两者都有）。
func normalizeModelName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	for {
		m := modelSuffix.FindStringIndex(n)
		if m == nil || m[0] == 0 {
			return n
		}
		n = n[:m[0]]
	}
}

// aliasResult 是一次别名解析的结果：Target 是之后交给执行层的模型名，Rule 是命中的规则（没走规则时为 nil）。
type aliasResult struct {
	Target string
	Rule   *aliasRule
}

// resolveModelAlias 按别名规则解析客户端的模型名（顺序见文件开头），known 判断号池目录里有没有某个模型。
func resolveModelAlias(name string, rules []aliasRule, known func(string) bool) aliasResult {
	if known(name) {
		return aliasResult{Target: name}
	}
	n := normalizeModelName(name)
	if n != name && known(n) {
		return aliasResult{Target: n}
	}
	for i := range rules {
		if globMatch(rules[i].Pattern, n) {
			return aliasResult{Target: rules[i].Target, Rule: &rules[i]}
		}
	}
	return aliasResult{Target: n}
}

// resolveModel 按当前生效的别名规则解析客户端的模型名，返回交给执行层的模型名。
func (s *Service) resolveModel(name string) string {
	return resolveModelAlias(name, s.aliases.list(), s.relay.KnownModel).Target
}

// loadModelAliases 启动时从库加载模型别名设置并生效。
func (s *Service) loadModelAliases(ctx context.Context) error {
	v, err := modelAliasSetting.load(ctx, s.store)
	if err != nil {
		return err
	}
	s.aliases.set(v.rules())
	return nil
}

func (s *Service) adminGetModelAliases(w http.ResponseWriter, r *http.Request) {
	v, err := modelAliasSetting.load(r.Context(), s.store)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": v, "presets": presetAliases})
}

func (s *Service) adminUpdateModelAliases(w http.ResponseWriter, r *http.Request) {
	v, ok := modelAliasSetting.update(w, r, s.store, modelAliasSetting.defaults())
	if !ok {
		return
	}
	s.aliases.set(v.rules())
	custom, _ := parseAliasRules(v.Rules)
	slog.InfoContext(r.Context(), "已修改模型别名设置", "use_presets", v.UsePresets, "custom_rules", len(custom))
	s.adminGetModelAliases(w, r)
}

// adminTestModelAlias 试解析一个模型名：显示命中的规则、交给执行层的模型名，以及按家族和档位最终用的模型。
func (s *Service) adminTestModelAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if !decodeAdminJSON(w, r, 1<<10, &req) {
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		adminError(w, http.StatusBadRequest, "model is required")
		return
	}
	res := resolveModelAlias(req.Model, s.aliases.list(), s.relay.KnownModel)
	out := map[string]any{"model": req.Model, "target": res.Target, "rule": res.Rule}
	if m, ok := s.relay.LookupModel(r.Context(), res.Target); ok {
		out["resolved"] = m.ID
	}
	writeJSON(w, http.StatusOK, out)
}
