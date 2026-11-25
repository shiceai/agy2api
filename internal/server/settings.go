package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/shiceai/agy2api/internal/antigravity"
)

// settings 表里的键。
const (
	// settingDefaultTier：家族名请求不带思考参数、该家族也没单独设置时用的档位。
	settingDefaultTier = "default_tier"
	// settingModelTiers：按家族单独设置的档位，JSON 对象 {家族名: 档位}。
	settingModelTiers = "model_tiers"
)

// settingsView 是管理端设置接口的读写形态。PUT 时整体替换：model_tiers 缺省表示清空。
type settingsView struct {
	DefaultTier string            `json:"default_tier"`
	ModelTiers  map[string]string `json:"model_tiers"`
}

// familyView 是一个分档家族及其可用档位，供前端选择。
type familyView struct {
	Family string   `json:"family"`
	Tiers  []string `json:"tiers"`
}

func validTier(t string) bool {
	return slices.Contains(antigravity.Tiers(), t)
}

// loadSettings 启动时从库加载设置并生效。库里的值不合法时记警告并忽略该值。
func (s *Service) loadSettings(ctx context.Context) error {
	tier, ok, err := s.store.GetSetting(ctx, settingDefaultTier)
	if err != nil {
		return err
	}
	if ok {
		if validTier(tier) {
			s.relay.SetDefaultTier(tier)
		} else {
			slog.WarnContext(ctx, "库里的默认档位不合法，改用内置默认值", "value", tier, "default", antigravity.DefaultTier)
		}
	}

	raw, ok, err := s.store.GetSetting(ctx, settingModelTiers)
	if err != nil || !ok {
		return err
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		slog.WarnContext(ctx, "库里的按模型档位不是合法 JSON，忽略", "err", err)
		return nil
	}
	tiers := map[string]string{}
	for family, t := range stored {
		if !validTier(t) {
			slog.WarnContext(ctx, "库里的按模型档位不合法，忽略该项", "family", family, "value", t)
			continue
		}
		tiers[family] = t
	}
	s.relay.SetModelTiers(tiers)
	return nil
}

func (s *Service) adminGetSettings(w http.ResponseWriter, r *http.Request) {
	families := make([]familyView, 0)
	for family, tiers := range s.relay.Families() {
		families = append(families, familyView{Family: family, Tiers: tiers})
	}
	sort.Slice(families, func(i, j int) bool { return families[i].Family < families[j].Family })
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settingsView{DefaultTier: s.relay.DefaultTier(), ModelTiers: s.relay.ModelTiers()},
		"tiers":    antigravity.Tiers(),
		"families": families,
	})
}

func (s *Service) adminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsView
	if !decodeAdminJSON(w, r, 1<<16, &req) {
		return
	}
	defaultTier := strings.ToLower(strings.TrimSpace(req.DefaultTier))
	if !validTier(defaultTier) {
		adminError(w, http.StatusBadRequest, fmt.Sprintf("default_tier must be one of %v", antigravity.Tiers()))
		return
	}
	modelTiers := map[string]string{}
	for family, t := range req.ModelTiers {
		family = strings.TrimSpace(family)
		t = strings.ToLower(strings.TrimSpace(t))
		if family == "" || !validTier(t) {
			adminError(w, http.StatusBadRequest, fmt.Sprintf("model_tiers[%q] must be one of %v", family, antigravity.Tiers()))
			return
		}
		modelTiers[family] = t
	}
	raw, err := json.Marshal(modelTiers)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.SetSettings(r.Context(), map[string]string{
		settingDefaultTier: defaultTier,
		settingModelTiers:  string(raw),
	}); err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.relay.SetDefaultTier(defaultTier)
	s.relay.SetModelTiers(modelTiers)
	slog.InfoContext(r.Context(), "已修改设置", "default_tier", defaultTier, "model_tiers", modelTiers)
	s.adminGetSettings(w, r)
}
