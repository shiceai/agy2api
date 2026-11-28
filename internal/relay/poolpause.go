package relay

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// 封禁后全池暂停（Policy.TOSPause，默认关）：有账号因条款封禁被停用时，整个号池暂停一段时间，期间不选任何账号：
// 生成请求直接返回 503（记一条用量，分类 KindPoolPaused），计数请求也拒绝，后台额度刷新停下。参考 CLIProxyAPI 的
// tos-pause-window。暂停截止时间存在 settings 表（poolPauseKey），重启后继续；到时自动结束，管理端可以提前恢复。

// poolPauseKey 是 settings 里存暂停状态的键。
const poolPauseKey = "pool_pause"

// KindPoolPaused 是号池暂停期间被拒绝的请求在用量里的失败分类。
const KindPoolPaused = "pool_paused"

// PoolPause 是一次全池暂停。
type PoolPause struct {
	Since     time.Time
	Until     time.Time
	AccountID int64 // 触发暂停的（第一个）被封账号
}

type poolPauseJSON struct {
	Since     int64 `json:"since"`
	Until     int64 `json:"until"`
	AccountID int64 `json:"account_id"`
}

// PoolPause 返回生效中的全池暂停，没有时为 nil。
func (r *Relay) PoolPause() *PoolPause {
	p := r.pause.Load()
	if p == nil || !time.Now().Before(p.Until) {
		return nil
	}
	return p
}

// LoadPoolPause 启动时读回没结束的全池暂停；已经过期的清掉。
func (r *Relay) LoadPoolPause(ctx context.Context) error {
	raw, ok, err := r.store.GetSetting(ctx, poolPauseKey)
	if err != nil || !ok {
		return err
	}
	var v poolPauseJSON
	if json.Unmarshal([]byte(raw), &v) != nil || v.Until == 0 {
		return nil
	}
	p := &PoolPause{Since: time.UnixMilli(v.Since), Until: time.UnixMilli(v.Until), AccountID: v.AccountID}
	if !time.Now().Before(p.Until) {
		r.persistPause(ctx, nil)
		return nil
	}
	slog.WarnContext(ctx, "号池仍在封禁后暂停中", "until", p.Until.Format(time.RFC3339), "account_id", p.AccountID)
	r.pauseMu.Lock()
	r.pause.Store(p)
	r.schedulePauseEndLocked(p)
	r.pauseMu.Unlock()
	return nil
}

// pauseAfterBan 在账号因条款封禁被停用后暂停号池（开了 Policy.TOSPause 时）。已在暂停中时延长到较晚的截止时间，
// 不重复通知。
func (r *Relay) pauseAfterBan(ctx context.Context, accountID int64) {
	p := r.Policy()
	if !p.TOSPause || p.TOSPauseWindow <= 0 {
		return
	}
	now := time.Now()
	next := &PoolPause{Since: now, Until: now.Add(p.TOSPauseWindow), AccountID: accountID}
	r.pauseMu.Lock()
	cur := r.pause.Load()
	active := cur != nil && now.Before(cur.Until)
	if active {
		next.Since, next.AccountID = cur.Since, cur.AccountID
		if cur.Until.After(next.Until) {
			next.Until = cur.Until
		}
	}
	r.pause.Store(next)
	r.schedulePauseEndLocked(next)
	r.pauseMu.Unlock()
	r.persistPause(ctx, next)
	slog.WarnContext(ctx, "有账号被条款封禁，号池暂停", "account_id", accountID, "until", next.Until.Format(time.RFC3339), "extended", active)
	if !active {
		r.emit(Event{Type: EventPoolPaused, AccountID: accountID, Until: next.Until})
	}
}

// ResumePool 提前结束全池暂停（管理端操作），没在暂停时返回 false。
func (r *Relay) ResumePool(ctx context.Context) bool {
	p := r.PoolPause()
	if p == nil {
		return false
	}
	return r.endPause(ctx, p, true)
}

// schedulePauseEndLocked 安排到时结束暂停。需持 r.pauseMu。
func (r *Relay) schedulePauseEndLocked(p *PoolPause) {
	if r.pauseTimer != nil {
		r.pauseTimer.Stop()
	}
	r.pauseTimer = time.AfterFunc(time.Until(p.Until), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.endPause(ctx, p, false)
	})
}

// endPause 结束暂停 p；p 已经不是生效中的那次（被延长替换、已结束）时什么都不做。
func (r *Relay) endPause(ctx context.Context, p *PoolPause, manual bool) bool {
	r.pauseMu.Lock()
	if r.pause.Load() != p {
		r.pauseMu.Unlock()
		return false
	}
	r.pause.Store(nil)
	if r.pauseTimer != nil {
		r.pauseTimer.Stop()
		r.pauseTimer = nil
	}
	r.pauseMu.Unlock()
	r.persistPause(ctx, nil)
	slog.InfoContext(ctx, "号池暂停结束", "manual", manual)
	r.emit(Event{Type: EventPoolResumed, AccountID: p.AccountID, Manual: manual})
	return true
}

// stopPauseTimer 在关闭时停掉到时结束的定时器（状态已落库，重启后读回）。
func (r *Relay) stopPauseTimer() {
	r.pauseMu.Lock()
	defer r.pauseMu.Unlock()
	if r.pauseTimer != nil {
		r.pauseTimer.Stop()
		r.pauseTimer = nil
	}
}

// persistPause 把暂停状态写库，p 为 nil 时清掉。不随请求取消。
func (r *Relay) persistPause(ctx context.Context, p *PoolPause) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	v := poolPauseJSON{}
	if p != nil {
		v = poolPauseJSON{Since: p.Since.UnixMilli(), Until: p.Until.UnixMilli(), AccountID: p.AccountID}
	}
	raw, _ := json.Marshal(v)
	if err := r.store.SetSetting(ctx, poolPauseKey, string(raw)); err != nil {
		slog.ErrorContext(ctx, "保存号池暂停状态失败", "err", err)
	}
}

// pausedError 在号池暂停中时返回给客户端的错误（503，Retry-After 到暂停结束），否则返回 nil。
func (r *Relay) pausedError() *Error {
	p := r.PoolPause()
	if p == nil {
		return nil
	}
	return &Error{
		Status:     http.StatusServiceUnavailable,
		Code:       "UNAVAILABLE",
		Message:    "the account pool is paused after an account was suspended for terms of service violation, retry after the pause ends",
		RetryAfter: retryAfter(time.Until(p.Until)),
	}
}

// rejectIfPaused 在号池暂停中时记一条用量并返回 503，否则返回 nil。
func (r *Relay) rejectIfPaused(ctx context.Context, req Request, start time.Time) *Error {
	e := r.pausedError()
	if e == nil {
		return nil
	}
	r.RecordRejected(ctx, req, start, e.Status, KindPoolPaused, e.Message)
	return e
}
