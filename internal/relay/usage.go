package relay

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/shiceai/agy2api/internal/store"
)

const (
	usageQueueSize    = 4096
	usageBatchSize    = 100
	usageFlushTimeout = 10 * time.Second
)

// usageWriter 把用量记录排队交给单个写协程批量落库。SQLite 只允许一个写入者，
// 单协程加批量事务，避免每个请求各开 goroutine 抢写锁。
// 队列满时丢弃新记录并记错误日志，不阻塞请求；Close 时写完队列里剩下的记录。
type usageWriter struct {
	store *store.Store
	queue chan *store.UsageRecord
	done  chan struct{}

	// mu 保护 closed 和关闭 queue，保证关闭后不会再往 queue 里写。
	mu     sync.RWMutex
	closed bool
}

func newUsageWriter(st *store.Store) *usageWriter {
	w := &usageWriter{
		store: st,
		queue: make(chan *store.UsageRecord, usageQueueSize),
		done:  make(chan struct{}),
	}
	go w.run()
	return w
}

// Enqueue 把一条记录放进写入队列，不阻塞。
func (w *usageWriter) Enqueue(rec *store.UsageRecord) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		slog.Error("服务正在关闭，丢弃用量记录", "request_id", rec.RequestID)
		return
	}
	select {
	case w.queue <- rec:
	default:
		slog.Error("用量写入队列已满，丢弃记录", "request_id", rec.RequestID)
	}
}

// Close 停止接收新记录，等写协程写完队列后返回；ctx 到期则不再等待。可重复调用。
func (w *usageWriter) Close(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run 每次取出一条记录，再把已排队的记录一起凑成一批（最多 usageBatchSize 条）写入。
func (w *usageWriter) run() {
	defer close(w.done)
	for rec := range w.queue {
		batch := append(make([]*store.UsageRecord, 0, usageBatchSize), rec)
	collect:
		for len(batch) < usageBatchSize {
			select {
			case next, ok := <-w.queue:
				if !ok {
					break collect
				}
				batch = append(batch, next)
			default:
				break collect
			}
		}
		w.flush(batch)
	}
}

func (w *usageWriter) flush(batch []*store.UsageRecord) {
	ctx, cancel := context.WithTimeout(context.Background(), usageFlushTimeout)
	defer cancel()
	if err := w.store.InsertUsage(ctx, batch...); err != nil {
		ids := make([]string, len(batch))
		for i, r := range batch {
			ids[i] = r.RequestID
		}
		slog.Error("写入用量记录失败", "count", len(batch), "request_ids", ids, "err", err)
	}
}
