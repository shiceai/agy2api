package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/shiceai/agy2api/internal/upgrade"
)

// systemView 是版本弹窗的数据：版本、新版本、升级进度，加上数据库结构的情况。
type systemView struct {
	upgrade.View
	// NewMigrations 是在线升级后新版本执行过的迁移数：撤销后回到的程序不认识这些结构。
	NewMigrations int `json:"new_migrations,omitempty"`
	// UnknownMigrations 是库里有、当前程序不认识的迁移：库被更新的版本改过结构，当前程序可能不兼容。
	UnknownMigrations []string `json:"unknown_migrations,omitempty"`
}

func (s *Service) systemView(ctx context.Context) systemView {
	v := systemView{View: s.upgrade.View(), UnknownMigrations: s.store.UnknownMigrations()}
	if v.InstalledAt > 0 {
		n, err := s.store.MigrationsSince(ctx, v.InstalledAt)
		if err != nil {
			slog.WarnContext(ctx, "读迁移记录失败", "err", err)
		}
		v.NewMigrations = n
	}
	return v
}

// RestartRequested 在在线升级或撤销完成、需要重启时关闭：main 关停服务后把进程换成要运行的程序。
func (s *Service) RestartRequested() <-chan struct{} { return s.upgrade.RestartRequested() }

// adminSystem 返回版本信息和升级进度，只读内存和本地文件，升级时页面轮询它。
func (s *Service) adminSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.systemView(r.Context()))
}

// autoCheckMaxAge 是自动检查新版本的间隔：管理端打开或切回前台时带 auto=1 来查，这段时间内查过就直接返回
// 上次的结果，多个页面同时开着也不会频繁连 GitHub。
const autoCheckMaxAge = 10 * time.Minute

// adminCheckUpdate 查一次新版本，失败的原因在返回的 check_error 里。手动检查 10 秒内重复点不重复查；
// auto=1 是管理端的自动检查，按 autoCheckMaxAge。
func (s *Service) adminCheckUpdate(w http.ResponseWriter, r *http.Request) {
	maxAge := 10 * time.Second
	if r.URL.Query().Get("auto") == "1" {
		maxAge = autoCheckMaxAge
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	_ = s.upgrade.Check(ctx, maxAge)
	writeJSON(w, http.StatusOK, s.systemView(r.Context()))
}

// adminUpgrade 开始升级到查到的最新版本，在后台进行，页面轮询 adminSystem 看进度；完成后服务自己重启。
func (s *Service) adminUpgrade(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if !decodeAdminJSON(w, r, 1<<10, &req) {
		return
	}
	if err := s.upgrade.Upgrade(req.Version); err != nil {
		adminError(w, http.StatusConflict, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "开始在线升级", "target", req.Version)
	writeJSON(w, http.StatusAccepted, s.systemView(r.Context()))
}

// adminRevertUpgrade 撤销在线升级：删掉数据目录里的新程序；正在运行它时重启回到自带的程序。
func (s *Service) adminRevertUpgrade(w http.ResponseWriter, r *http.Request) {
	if _, err := s.upgrade.Revert(); err != nil {
		adminError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.systemView(r.Context()))
}
