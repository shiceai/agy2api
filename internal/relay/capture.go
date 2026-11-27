package relay

import (
	"bytes"
	"compress/gzip"
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shiceai/agy2api/internal/store"
)

// 失败请求正文的保存（"设置 → 数据 → 保存失败请求的正文"，默认关）：上游返回了错误的请求，把实际发给上游的
// 请求体（含信封）gzip 压缩后存进 request_bodies，管理端在用量详情里下载，排查上游为什么拒绝。含用户的对话内容。
//
// 只在失败时做，成功的请求不受影响；压缩和写库在后台协程里，不拖慢给客户端的响应。上游大面积出错时每个请求都失败，
// 所以每分钟最多存 CapturePerMinute 条、排队最多 captureQueueSize 条，超出的跳过（用量详情里标"未保存"）。
// 存下的只留最近 CaptureMaxCount 条、总占用不超过 CaptureMaxBytes，超过用量保留期的随用量记录一起删。
const (
	CapturePerMinute = 10
	captureQueueSize = 4
	CaptureMaxCount  = 200
	CaptureMaxBytes  = 500 << 20
	captureWindow    = time.Minute
)

type captureJob struct {
	requestID string
	body      []byte
	at        time.Time
}

// capturer 是失败请求正文的后台保存。enabled 由 SetRequestCapture 切换。
type capturer struct {
	store   *store.Store
	enabled atomic.Bool

	mu     sync.Mutex
	recent []time.Time // 最近 captureWindow 内接受的时间，限速用
	closed bool
	queue  chan captureJob
	done   chan struct{}
}

func newCapturer(st *store.Store) *capturer {
	c := &capturer{store: st, queue: make(chan captureJob, captureQueueSize), done: make(chan struct{})}
	go c.run()
	return c
}

// offer 在请求失败时调用：没开时返回空；开着时按限速和队列决定存不存，返回 store.RequestBodySaved 或
// store.RequestBodySkipped。body 交给后台后不能再改（调用方之后不再用它）。不阻塞。
func (c *capturer) offer(requestID string, body []byte, now time.Time) string {
	if !c.enabled.Load() || requestID == "" || len(body) == 0 {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return store.RequestBodySkipped
	}
	keep := c.recent[:0]
	for _, t := range c.recent {
		if now.Sub(t) < captureWindow {
			keep = append(keep, t)
		}
	}
	c.recent = keep
	if len(c.recent) >= CapturePerMinute {
		return store.RequestBodySkipped
	}
	select {
	case c.queue <- captureJob{requestID: requestID, body: body, at: now}:
		c.recent = append(c.recent, now)
		return store.RequestBodySaved
	default:
		return store.RequestBodySkipped
	}
}

// close 停止接收，等队列里的写完；ctx 到期则不再等待。可重复调用。
func (c *capturer) close(ctx context.Context) error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.queue)
	}
	c.mu.Unlock()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *capturer) run() {
	defer close(c.done)
	for job := range c.queue {
		c.save(job)
	}
}

// save 压缩、写库，再按条数和总占用修剪。失败只记日志。
func (c *capturer) save(job captureJob) {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if _, err := zw.Write(job.body); err != nil || zw.Close() != nil {
		slog.Error("压缩失败请求的正文失败", "request_id", job.requestID, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.store.SaveRequestBody(ctx, job.requestID, buf.Bytes(), int64(len(job.body)), job.at.UnixMilli()); err != nil {
		slog.Error("保存失败请求的正文失败", "request_id", job.requestID, "err", err)
		return
	}
	if n, err := c.store.PruneRequestBodies(ctx, CaptureMaxCount, CaptureMaxBytes); err != nil {
		slog.Error("修剪失败请求的正文失败", "err", err)
	} else if n > 0 {
		slog.Debug("修剪失败请求的正文", "deleted", n)
	}
}

// SetRequestCapture 开关失败请求正文的保存，立即生效。
func (r *Relay) SetRequestCapture(on bool) {
	r.capture.enabled.Store(on)
}
