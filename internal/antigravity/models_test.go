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
