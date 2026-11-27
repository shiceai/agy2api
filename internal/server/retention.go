package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// 用量记录清理：超过保留期的用量记录整行删除（含详情）。删之前把它们的 Token 按 Key 归档（见
// store.PurgeUsageBatch），Key 的总量上限不会因此变少。保留期至少 32 天：Key 的每月上限要看完整的本月记录，
// 用量页、Key 详情最长看 30 天。
//
// 后台每 purgeScan 检查一次，距上次清理超过 purgeInterval 才执行；按 id 分批删，每批一个事务，批间停一下，
// 不长时间占住写锁（新的用量记录要用同一个写连接）。SQLite 删数据后文件不会变小，空出来的页会被新数据复用。

const (
	// settingPurge：最近一次清理的结果（purgeResult），JSON。
	settingPurge = "usage_purge"

	retentionDefaultDays = 90
	retentionMinDays     = 32
	retentionMaxDays     = 3650

	purgeBatch      = 2000
	purgePause      = 100 * time.Millisecond
	purgeInterval   = 24 * time.Hour
	purgeScan       = time.Hour
	purgeStartDelay = 10 * time.Minute
)

var errPurgeRunning = errors.New("正在清理，稍后再试")

// retentionSettings 是用量记录的保留设置：Enabled 为 false 时不自动清理（永久保留）。
type retentionSettings struct {
	Enabled bool `json:"enabled"`
	Days    int  `json:"days"`
}

// retentionSetting 存在 settings 的 usage_retention 键。默认开启、保留 90 天。
var retentionSetting = jsonSetting[retentionSettings]{
	key:      "usage_retention",
	name:     "用量保留设置",
	limit:    1 << 10,
	defaults: func() retentionSettings { return retentionSettings{Enabled: true, Days: retentionDefaultDays} },
	validate: retentionSettings.validate,
}

func (v retentionSettings) validate() error {
	if v.Days < retentionMinDays || v.Days > retentionMaxDays {
		return fmt.Errorf("保留天数应在 %d～%d", retentionMinDays, retentionMaxDays)
	}
	return nil
}

// purgeResult 是一次清理的结果。
type purgeResult struct {
	At         int64  `json:"at"`
	Days       int    `json:"days"`
	Deleted    int64  `json:"deleted"`
	DurationMs int64  `json:"duration_ms"`
	Manual     bool   `json:"manual"`
	Error      string `json:"error,omitempty"`
}

// retentionView 是设置页"数据"的用量记录卡片：设置、现状（条数、最早一条、超过保留期的条数、数据库文件大小）、
// 最近一次清理。
type retentionView struct {
	Settings retentionSettings `json:"settings"`
	Records  int64             `json:"records"`
	OldestAt int64             `json:"oldest_at"`
	Expired  int64             `json:"expired"`
	DBBytes  int64             `json:"db_bytes"`
	Last     *purgeResult      `json:"last"`
	Running  bool              `json:"running"`
}

// retentionSettings 读保留设置；读库出错时按默认值。
func (s *Service) retentionSettings(ctx context.Context) retentionSettings {
	v, err := retentionSetting.load(ctx, s.store)
	if err != nil {
		slog.WarnContext(ctx, "读取用量保留设置失败，按默认值", "err", err)
	}
	return v
}

func (s *Service) lastPurge(ctx context.Context) *purgeResult {
	raw, ok, err := s.store.GetSetting(ctx, settingPurge)
	if err != nil || !ok {
		return nil
	}
	var r purgeResult
	if json.Unmarshal([]byte(raw), &r) != nil {
		return nil
	}
	return &r
}

func cutoffFor(days int, now time.Time) int64 {
	return now.AddDate(0, 0, -days).UnixMilli()
}

// dbBytes 是数据库文件（含 WAL）的大小。
func (s *Service) dbBytes() int64 {
	var n int64
	for _, name := range []string{"agy2api.db", "agy2api.db-wal", "agy2api.db-shm"} {
		if fi, err := os.Stat(filepath.Join(s.cfg.DataDir, name)); err == nil {
			n += fi.Size()
		}
	}
	return n
}

func (s *Service) retentionView(ctx context.Context) (*retentionView, error) {
	set := s.retentionSettings(ctx)
	st, err := s.store.UsageStorageStats(ctx, cutoffFor(set.Days, time.Now()))
	if err != nil {
		return nil, err
	}
	return &retentionView{Settings: set, Records: st.Records, OldestAt: st.OldestAt, Expired: st.Expired,
		DBBytes: s.dbBytes(), Last: s.lastPurge(ctx), Running: s.purging.Load()}, nil
}

// purgeUsage 删除早于 days 天前的用量记录，记下结果。后台和手动不同时跑。
func (s *Service) purgeUsage(ctx context.Context, days int, manual bool) (purgeResult, error) {
	if !s.purging.CompareAndSwap(false, true) {
		return purgeResult{}, errPurgeRunning
	}
	defer s.purging.Store(false)
	start := time.Now()
	cutoff := cutoffFor(days, start)
	res := purgeResult{At: start.UnixMilli(), Days: days, Manual: manual}
	var err error
	for {
		var n int64
		if n, err = s.store.PurgeUsageBatch(ctx, cutoff, purgeBatch); err != nil || n == 0 {
			break
		}
		res.Deleted += n
		select {
		case <-s.bgStop:
			err = errors.New("服务正在关闭，清理中断")
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(purgePause):
			continue
		}
		break
	}
	// 失败请求的正文随用量记录一起按保留期删。
	if err == nil {
		if n, derr := s.store.DeleteRequestBodiesBefore(ctx, cutoff); derr != nil {
			slog.ErrorContext(ctx, "清理失败请求的正文失败", "err", derr)
		} else if n > 0 {
			slog.InfoContext(ctx, "已清理超过保留期的失败请求正文", "deleted", n)
		}
	}
	res.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		slog.ErrorContext(ctx, "清理用量记录失败", "deleted", res.Deleted, "err", err)
	} else {
		slog.InfoContext(ctx, "已清理用量记录", "days", days, "deleted", res.Deleted, "duration_ms", res.DurationMs, "manual", manual)
	}
	raw, _ := json.Marshal(res)
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if serr := s.store.SetSetting(wctx, settingPurge, string(raw)); serr != nil {
		slog.ErrorContext(wctx, "保存清理结果失败", "err", serr)
	}
	return res, err
}

// startUsagePurge 启动后台清理：启动 purgeStartDelay 后第一次检查，之后每 purgeScan 一次，
// 开着自动清理且距上次清理超过 purgeInterval 才执行。
func (s *Service) startUsagePurge() {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		t := time.NewTimer(purgeStartDelay)
		defer t.Stop()
		for {
			select {
			case <-s.bgStop:
				return
			case <-t.C:
				s.purgeIfDue()
				t.Reset(purgeScan)
			}
		}
	}()
}

func (s *Service) purgeIfDue() {
	ctx := context.Background()
	set := s.retentionSettings(ctx)
	if !set.Enabled {
		return
	}
	if last := s.lastPurge(ctx); last != nil && time.Since(time.UnixMilli(last.At)) < purgeInterval {
		return
	}
	if _, err := s.purgeUsage(ctx, set.Days, false); err != nil && !errors.Is(err, errPurgeRunning) {
		slog.Error("后台清理用量记录失败", "err", err)
	}
}

func (s *Service) writeRetention(w http.ResponseWriter, r *http.Request) {
	v, err := s.retentionView(r.Context())
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) adminGetRetention(w http.ResponseWriter, r *http.Request) {
	s.writeRetention(w, r)
}

// adminUpdateRetention 保存保留设置（自动清理开关、保留天数 32～3650）。
func (s *Service) adminUpdateRetention(w http.ResponseWriter, r *http.Request) {
	req, ok := retentionSetting.update(w, r, s.store, retentionSetting.defaults())
	if !ok {
		return
	}
	slog.InfoContext(r.Context(), "已修改用量保留设置", "enabled", req.Enabled, "days", req.Days)
	s.writeRetention(w, r)
}

// adminRunPurge 立即按保存的保留天数清理一次（不管自动清理开没开），完成后返回最新的现状。
// 不随请求取消：已删的批次已经提交，半途中断也只是少删一些。
func (s *Service) adminRunPurge(w http.ResponseWriter, r *http.Request) {
	set := s.retentionSettings(r.Context())
	if _, err := s.purgeUsage(context.WithoutCancel(r.Context()), set.Days, true); err != nil {
		if errors.Is(err, errPurgeRunning) {
			adminError(w, http.StatusConflict, err.Error())
			return
		}
		adminError(w, http.StatusInternalServerError, "清理失败："+err.Error())
		return
	}
	s.writeRetention(w, r)
}
