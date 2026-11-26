package antigravity

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"
)

// catalogFixture 是按 2026-11-26 实抓的 fetchAvailableModels 裁出来的目录：分档家族、不分档的选择器模型、
// 辅助用途的模型，以及官方界面不显示的（旧名字、自动分档变体、补全模型）。
const catalogFixture = `{
	"models": {
		"gemini-4-neon-high": {"displayName": "Gemini 4 Neon (High)", "model": "M1"},
		"gemini-4-neon-low": {"displayName": "Gemini 4 Neon (Low)", "model": "M2"},
		"gemini-4-neon-xhigh": {"displayName": "Gemini 4 Neon (XHigh)", "model": "M3"},
		"claude-opus-5-5-medium": {"displayName": "Claude Opus 5.5 (Medium)", "model": "M4"},
		"gemini-pro-orchestrator": {"displayName": "Orchestrator (Carbon)", "model": "M5"},
		"gemini-3-flash": {"displayName": "Gemini 3 Flash", "model": "M6"},
		"gemini-3.5-flash-lite": {"displayName": "Gemini 3.5 Flash Lite", "model": "M7"},
		"gemini-3.1-flash-image": {"displayName": "Gemini 3.1 Flash Image", "model": "M8"},
		"gemini-2.5-flash": {"displayName": "Gemini 3.5 Flash Lite", "model": "M7"},
		"gemini-3.8-flash-tiered": {"model": "M9"},
		"tab_flash_lite_preview": {"model": "M10"},
		"chat_1": {"model": "M11", "isInternal": true}
	},
	"agentModelSorts": [{"displayName": "Recommended", "groups": [{"modelIds": [
		"gemini-4-neon-high", "gemini-4-neon-xhigh", "gemini-4-neon-low", "gemini-pro-orchestrator", "claude-opus-5-5-medium"
	]}]}],
	"groupedModels": {
		"gemini-4-neon": {"displayName": "Gemini 4 Neon", "supportedThinkingLevels": [
			{"thinkingSuffix": "low"}, {"thinkingSuffix": "medium"}, {"thinkingSuffix": "high"}, {"thinkingSuffix": "xhigh"}
		]},
		"claude-opus-5-5": {"displayName": "Claude Opus 5.5", "supportedThinkingLevels": [{"thinkingSuffix": "medium"}]}
	},
	"commandModelIds": ["gemini-3-flash"],
	"imageGenerationModelIds": ["gemini-3.1-flash-image"],
	"webSearchModelIds": ["gemini-3.5-flash-lite"],
	"commitMessageModelIds": ["gemini-3.5-flash-lite"],
	"tieredModelIds": {"flash": ["gemini-3.8-flash-tiered"]}
}`

// wantPublic 是 catalogFixture 对外列出的模型：选择器和辅助用途里的，分档的合成家族名（显示名用家族的）；
// 旧名字、自动分档变体、补全模型不列。
const wantPublic = "[claude-opus-5-5=Claude Opus 5.5|claude-opus-5-5-medium " +
	"gemini-3-flash=Gemini 3 Flash " +
	"gemini-3.1-flash-image=Gemini 3.1 Flash Image " +
	"gemini-3.5-flash-lite=Gemini 3.5 Flash Lite " +
	"gemini-4-neon=Gemini 4 Neon|gemini-4-neon-high|gemini-4-neon-low|gemini-4-neon-xhigh " +
	"gemini-pro-orchestrator=Orchestrator (Carbon)]"

// parseCatalogFixture 按 FetchAvailableModels 的做法解析目录：去掉内部模型，标出对外列出的，按 ID 排序。
func parseCatalogFixture(t *testing.T) []ModelInfo {
	t.Helper()
	var resp fetchModelsResponse
	if err := json.Unmarshal([]byte(catalogFixture), &resp); err != nil {
		t.Fatal(err)
	}
	var models []ModelInfo
	for id, m := range resp.Models {
		if !m.IsInternal {
			models = append(models, ModelInfo{ID: id, DisplayName: m.DisplayName})
		}
	}
	resp.markListed(models)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

// listedIDs 把列表写成"ID=显示名|档位…"，便于比较。
func listedIDs(list []ListedModel) string {
	var out []string
	for _, m := range list {
		item := m.ID + "=" + m.DisplayName
		for _, t := range m.Tiers {
			item += "|" + t.ID
		}
		out = append(out, item)
	}
	return fmt.Sprint(out)
}

func TestPublicModels(t *testing.T) {
	if got := listedIDs(PublicModels(parseCatalogFixture(t))); got != wantPublic {
		t.Fatalf("PublicModels =\n%s\nwant\n%s", got, wantPublic)
	}
}

// 存库再读回来，列出的标记和家族都在。
func TestListedSurvivesStorage(t *testing.T) {
	models := UnmarshalModels(MarshalModels(parseCatalogFixture(t)))
	if got := listedIDs(PublicModels(models)); got != wantPublic {
		t.Fatalf("PublicModels after storage =\n%s", got)
	}
}

// 目录里没有这些信息（升级前存下、还没刷新的目录）时列出全部，和以前一样。
func TestPublicModelsWithoutListing(t *testing.T) {
	catalog := []ModelInfo{{ID: "gemini-3-pro-high", DisplayName: "Gemini 3 Pro (High)"}, {ID: "gemini-3-pro-low"}}
	if got := listedIDs(PublicModels(catalog)); got != "[gemini-3-pro-high=Gemini 3 Pro (High) gemini-3-pro-low=]" {
		t.Fatalf("PublicModels = %s", got)
	}
}

// 每周额度对应到模型：组说明里列了的按列的（几个组都列了取最具体的），没列的辅助模型按厂商归到总组；
// 没有每周窗口的组不算；上游省略了比例但有重置时间的按用完算。
func TestWeeklyLimits(t *testing.T) {
	r := func(f float64) *float64 { return &f }
	summary := &QuotaSummary{Groups: []QuotaGroup{
		{DisplayName: "Gemini Models", Description: "Models within this group: Gemini 4 Neon, Gemini 4 Argon, Orchestrator (Neon)",
			Buckets: []QuotaBucket{{Window: "5h", RemainingFraction: r(0.9)}, {Window: "weekly", RemainingFraction: r(0.6), ResetTime: "2030-01-02T00:00:00Z"}}},
		{DisplayName: "Gemini 4 Argon", Description: "Models within this group: Gemini 4 Argon",
			Buckets: []QuotaBucket{{Window: "weekly", ResetTime: "2030-01-03T00:00:00Z"}}},
		{DisplayName: "Claude and GPT models", Description: "Models within this group: Claude Opus 5.5, GPT-OSS 120B",
			Buckets: []QuotaBucket{{Window: "weekly", RemainingFraction: r(0.3), ResetTime: "2030-01-04T00:00:00Z"}}},
		{DisplayName: "Other", Buckets: []QuotaBucket{{Window: "5h", RemainingFraction: r(0.1)}}},
	}}
	models := []ModelInfo{
		{ID: "gemini-4-neon-high", DisplayName: "Gemini 4 Neon (High)", Family: "gemini-4-neon", FamilyDisplayName: "Gemini 4 Neon"},
		{ID: "gemini-4-argon-low", DisplayName: "Gemini 4 Argon (Low)"},
		{ID: "gemini-pro-orchestrator-neon", DisplayName: "Orchestrator (Neon)"},
		{ID: "gemini-3-flash", DisplayName: "Gemini 3 Flash"},
		{ID: "claude-opus-5-5-medium", DisplayName: "Claude Opus 5.5 (Medium)"},
		{ID: "gpt-oss-120b-medium", DisplayName: "GPT-OSS 120B (Medium)"},
		{ID: "tab_flash_lite_preview"},
	}
	got := WeeklyLimits(summary, models)
	want := map[string]string{
		"gemini-4-neon-high":           "0.6 2030-01-02",
		"gemini-4-argon-low":           "0 2030-01-03", // 两组都列了 Argon，取只列它的那组；比例省略按用完算
		"gemini-pro-orchestrator-neon": "0.6 2030-01-02",
		"gemini-3-flash":               "0.6 2030-01-02", // 没列，按厂商归到 Gemini 总组
		"claude-opus-5-5-medium":       "0.3 2030-01-04",
		"gpt-oss-120b-medium":          "0.3 2030-01-04",
	}
	if len(got) != len(want) {
		t.Fatalf("WeeklyLimits = %v", got)
	}
	for id, w := range want {
		l, ok := got[id]
		if s := fmt.Sprintf("%v %s", l.Remaining, l.Reset.Format("2006-01-02")); !ok || s != w {
			t.Errorf("%s = %s, want %s", id, s, w)
		}
	}
	if WeeklyLimits(nil, models) != nil {
		t.Fatal("没有分组额度时应为 nil")
	}
}
