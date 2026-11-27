package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
)

// 官方 Antigravity 客户端版本监控：后台定时查官方更新清单的最新版本，和内置 UA 用的最高版本比。
// 落后了在管理端提示去发版更新 UA（CL 要实读核验，不自动切到未核验的版本，见 antigravity/profile.go）。

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
				s.checkHubVersion()
				t.Reset(hubVersionCheckInterval)
			}
		}
	}()
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
