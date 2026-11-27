package relay

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/store"
)

// 被拒上下文拦截（"设置 → 请求处理 → 被拒上下文拦截"，默认开）：请求被判为"请求被拒"（KindRequestRejected，
// 上游按内容拒绝的典型表现）时，记下它对话内容的上下文指纹（antigravity.ContextFingerprints 的最后一个）。之后的请求
// 只要任一前缀的指纹等于它，就说明包含这段被拒的上下文：原样重试、在后面继续对话都一样会被拒，在本地直接返回 400，
// 不再发往上游，少让账号反复碰被拒的内容。删改了相关内容的、新开的对话指纹不同，照常发往上游。
//
// 指纹在内存里查（热路径不读库），落库在后台；只存哈希、不存原文。超过 RejectedContextTTL 没再拦到的不再生效
// 并从库里删除，最多留 RejectedContextMax 条。
const (
	KindContextRejected = "context_rejected"
	RejectedContextTTL  = 7 * 24 * time.Hour
	RejectedContextMax  = 10000
)

// rejectedContexts 是被拒上下文的指纹集合，可并发使用。
type rejectedContexts struct {
	store   *store.Store
	enabled atomic.Bool

	mu   sync.RWMutex
	last map[string]time.Time // 指纹 → 最近一次被拒或被拦的时间

	// writes 是排队的写库操作，由一个后台协程按顺序执行：先记下的被拒上下文一定先于之后的命中写入。
	writeMu sync.Mutex
	closed  bool
	writes  chan func(context.Context) error
	done    chan struct{}
}

// rejectedWriteQueue 是排队写库的上限；排满时丢弃（只少记一次命中或续期，内存里照常生效）。
const rejectedWriteQueue = 256

func newRejectedContexts(st *store.Store) *rejectedContexts {
	rc := &rejectedContexts{store: st, last: map[string]time.Time{},
		writes: make(chan func(context.Context) error, rejectedWriteQueue), done: make(chan struct{})}
	rc.enabled.Store(true)
	go rc.run()
	return rc
}

// match 返回 body 命中的被拒指纹，没有命中、没开或集合为空时返回空。没有指纹时不计算，平时没有额外开销。
func (rc *rejectedContexts) match(body map[string]any, now time.Time) string {
	if !rc.enabled.Load() {
		return ""
	}
	rc.mu.RLock()
	empty := len(rc.last) == 0
	rc.mu.RUnlock()
	if empty {
		return ""
	}
	fps := antigravity.ContextFingerprints(body)
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	for _, fp := range fps {
		if at, ok := rc.last[fp]; ok && now.Sub(at) < RejectedContextTTL {
			return fp
		}
	}
	return ""
}

// touch 把指纹的最近时间改成 now（加入或续期）。
func (rc *rejectedContexts) touch(fp string, now time.Time) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.last[fp] = now
}

// load 从库里读回指纹（先删掉过期和超量的）。启动时和管理端删除后调用。
func (rc *rejectedContexts) load(ctx context.Context, now time.Time) error {
	if _, err := rc.store.PruneRejectedContexts(ctx, now.Add(-RejectedContextTTL).UnixMilli(), RejectedContextMax); err != nil {
		return err
	}
	list, err := rc.store.RejectedContexts(ctx)
	if err != nil {
		return err
	}
	last := make(map[string]time.Time, len(list))
	for _, c := range list {
		last[c.Fingerprint] = time.UnixMilli(c.LastHitAt)
	}
	rc.mu.Lock()
	rc.last = last
	rc.mu.Unlock()
	return nil
}

// persist 把一次写库交给后台协程（不随请求取消），不阻塞。
func (rc *rejectedContexts) persist(write func(context.Context) error) {
	rc.writeMu.Lock()
	defer rc.writeMu.Unlock()
	if rc.closed {
		return
	}
	select {
	case rc.writes <- write:
	default:
		slog.Warn("被拒上下文的写库队列已满，丢弃一次写入")
	}
}

// run 按顺序执行写库，每次写完按过期和条数修剪；修剪掉了的话重新读回，内存和库一致。
func (rc *rejectedContexts) run() {
	defer close(rc.done)
	for write := range rc.writes {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := write(ctx); err != nil {
			slog.Error("保存被拒上下文失败", "err", err)
		} else {
			now := time.Now()
			n, err := rc.store.PruneRejectedContexts(ctx, now.Add(-RejectedContextTTL).UnixMilli(), RejectedContextMax)
			if err != nil {
				slog.Error("修剪被拒上下文失败", "err", err)
			} else if n > 0 {
				if err := rc.load(ctx, now); err != nil {
					slog.Error("读回被拒上下文失败", "err", err)
				}
			}
		}
		cancel()
	}
}

// close 停止接收写入，等排队的写完；ctx 到期则不再等待。可重复调用。
func (rc *rejectedContexts) close(ctx context.Context) error {
	rc.writeMu.Lock()
	if !rc.closed {
		rc.closed = true
		close(rc.writes)
	}
	rc.writeMu.Unlock()
	select {
	case <-rc.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// rememberRejected 在请求被判为"请求被拒"后记下它的上下文指纹。没开时不记。
func (r *Relay) rememberRejected(ctx context.Context, req Request) {
	rc := r.rejected
	if !rc.enabled.Load() {
		return
	}
	fps := antigravity.ContextFingerprints(req.Body)
	if len(fps) == 0 {
		return
	}
	now := time.Now()
	c := store.RejectedContext{Fingerprint: fps[len(fps)-1], Turns: len(fps), APIKeyID: req.APIKeyID, Model: req.requestModel(),
		ClientIP: req.ClientIP, RequestID: logx.RequestID(ctx), CreatedAt: now.UnixMilli()}
	rc.touch(c.Fingerprint, now)
	slog.InfoContext(ctx, "记下被拒的上下文，之后包含它的请求在本地拒绝", "fingerprint", c.Fingerprint, "turns", c.Turns)
	rc.persist(func(ctx context.Context) error { return r.store.AddRejectedContext(ctx, c) })
}

// blockRejectedContext 在请求包含被拒的上下文时记一条用量并返回 400，不选号、不发上游；否则返回 nil。
func (r *Relay) blockRejectedContext(ctx context.Context, req Request, start time.Time) *Error {
	rc := r.rejected
	fp := rc.match(req.Body, start)
	if fp == "" {
		return nil
	}
	const msg = "upstream previously rejected a request containing this conversation context; " +
		"edit or remove the related content, or start a new conversation"
	slog.InfoContext(ctx, "请求包含被拒的上下文，本地拒绝", "fingerprint", fp, "model", req.Model)
	rc.touch(fp, start)
	rc.persist(func(ctx context.Context) error { return r.store.HitRejectedContext(ctx, fp, start.UnixMilli()) })
	r.RecordRejected(ctx, req, start, http.StatusBadRequest, KindContextRejected, msg)
	return &Error{Status: http.StatusBadRequest, Code: "FAILED_PRECONDITION", Message: msg}
}

// SetContextBlock 开关被拒上下文拦截，立即生效；关闭后不记新的、也不拦，已记的保留。
func (r *Relay) SetContextBlock(on bool) {
	r.rejected.enabled.Store(on)
}

// LoadRejectedContexts 从库读回被拒的上下文。启动时和管理端删除、清空后调用。
func (r *Relay) LoadRejectedContexts(ctx context.Context) error {
	return r.rejected.load(ctx, time.Now())
}
