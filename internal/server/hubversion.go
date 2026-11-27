package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
)

// 官方 Antigravity 客户端版本监控与 UA 构建表刷新：后台定时先从仓库拉 ua-builds.json 刷新对外声明的
// (版本, CL) 表（版本/CL 与发版脱钩，见 antigravity/builds.go），再查官方更新清单的最新版本和刷新后的
// 最高版本比，落后了在管理端提示（ua-builds.json 该补新对；CL 由流水线静态读官方构建戳维护）。

const (
	// hubVersionCheckDelay 是启动后第一次查的延迟，避开启动高峰；hubVersionCheckInterval 是之后的间隔。
	hubVersionCheckDelay    = 30 * time.Second
	hubVersionCheckInterval = 3 * time.Hour
	hubVersionFetchTimeout  = 10 * time.Second
)

// antigravityHubView 是官方客户端版本情况，挂在 systemView 里给管理端。
type antigravityHubView struct {
	// Shipped 是内置 UA 用的最高版本，始终有值。
	Shipped string `json:"shipped"`
	// Latest 是官方最新版本，查到了才有。
	Latest string `json:"latest,omitempty"`
	// Outdated 为真表示官方比内置新，建议发版更新 UA。
	Outdated bool `json:"outdated"`
	// CheckedAt 是最近一次查到官方版本的时间（Unix 毫秒），查到了才有。
	CheckedAt int64 `json:"checked_at,omitempty"`
}

func (s *Service) startHubVersionCheck() {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		t := time.NewTimer(hubVersionCheckDelay)
		defer t.Stop()
		for {
			select {
			case <-s.bgStop:
				return
			case <-t.C:
				s.refreshUABuilds()
				s.checkHubVersion()
				t.Reset(hubVersionCheckInterval)
			}
		}
	}()
}

// refreshUABuilds 从仓库拉取 ua-builds.json 刷新 UA 的 (版本, CL) 表；失败或内容无效时保留当前表。
// 关闭刷新或配置了统一 UserAgent 时不做（统一 UA 下活动表不参与派生）。
func (s *Service) refreshUABuilds() {
	if !s.cfg.UABuildsRefresh || s.cfg.UserAgent != "" {
		return
	}
	url := s.cfg.UABuildsURL
	if url == "" {
		url = antigravity.DefaultUABuildsURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), hubVersionFetchTimeout)
	defer cancel()
	builds, err := antigravity.FetchRemoteBuilds(ctx, &http.Client{Timeout: hubVersionFetchTimeout}, url)
	if err != nil {
		slog.WarnContext(ctx, "拉取 ua-builds 失败", "err", err)
		return
	}
	before := antigravity.LatestShippedHubVersion()
	if err := antigravity.SetActiveBuilds(builds); err != nil {
		slog.WarnContext(ctx, "ua-builds 内容无效，保留当前 UA 构建表", "err", err)
		return
	}
	if after := antigravity.LatestShippedHubVersion(); after != before {
		slog.InfoContext(ctx, "UA 声明版本更新", "from", before, "to", after)
	}
}

// checkHubVersion 查一次官方最新版本并记下来；失败只记日志，保留上次的结果。
func (s *Service) checkHubVersion() {
	ctx, cancel := context.WithTimeout(context.Background(), hubVersionFetchTimeout)
	defer cancel()
	v, err := antigravity.FetchLatestHubVersion(ctx, &http.Client{Timeout: hubVersionFetchTimeout})
	if err != nil {
		slog.WarnContext(ctx, "查官方 Antigravity 版本失败", "err", err)
		return
	}
	s.hubVersionMu.Lock()
	s.hubVersion = v
	s.hubVersionAt = time.Now()
	s.hubVersionMu.Unlock()
	if antigravity.HubVersionNewer(v, antigravity.LatestShippedHubVersion()) {
		slog.InfoContext(ctx, "官方 Antigravity 客户端已更新，内置 UA 版本偏旧", "latest", v, "shipped", antigravity.LatestShippedHubVersion())
	}
}

// hubView 返回官方客户端版本情况，供 systemView 使用。
func (s *Service) hubView() antigravityHubView {
	shipped := antigravity.LatestShippedHubVersion()
	s.hubVersionMu.Lock()
	latest, at := s.hubVersion, s.hubVersionAt
	s.hubVersionMu.Unlock()
	v := antigravityHubView{Shipped: shipped}
	if latest != "" {
		v.Latest = latest
		v.Outdated = antigravity.HubVersionNewer(latest, shipped)
		v.CheckedAt = at.UnixMilli()
	}
	return v
}
