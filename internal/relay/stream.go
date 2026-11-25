package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
)

// 上游调用的时间限制：
//   - 非流式：整个调用最多 nonStreamTimeout（假流式也是非流式，长输出要留余量）。
//   - 流式：等上游返回响应头最多 streamHeaderTimeout（上游的响应头和第一帧几乎同时到，这段就是等第一帧）；
//     之后不限总时长，改看空闲：两次从上游读到数据之间超过 Policy.StreamIdleTimeout 就断开（0 表示不断开）。
const (
	nonStreamTimeout    = 10 * time.Minute
	streamHeaderTimeout = 5 * time.Minute
)

// ErrStreamIdle 表示流式响应中途上游太久没有数据，已断开。读流式响应体时返回它，调用方据此给客户端发错误事件。
var ErrStreamIdle = errors.New("relay: upstream stream idle timeout")

// KindStreamIdle 是流式中途空闲断开在用量详情里的原因分类。
const KindStreamIdle = "stream_idle"

// errHeaderTimeout 是流式请求在 streamHeaderTimeout 内没等到响应头时的错误（取消上游请求后，http 客户端
// 只报 context canceled，看不出原因）。
var errHeaderTimeout = fmt.Errorf("upstream returned no response headers within %s", streamHeaderTimeout)

// startCall 为一次上游调用准备 ctx：非流式带总超时；流式只在等响应头期间计时，响应头到了调用 headerArrived 停表。
// 返回的 timedOut 报告是否因为等响应头超时被取消。
func startCall(ctx context.Context, stream bool) (callCtx context.Context, cancel context.CancelFunc, headerArrived func(), timedOut func() bool) {
	if !stream {
		callCtx, cancel = context.WithTimeout(ctx, nonStreamTimeout)
		return callCtx, cancel, func() {}, func() bool { return false }
	}
	callCtx, cancel = context.WithCancel(ctx)
	var fired atomic.Bool
	timer := time.AfterFunc(streamHeaderTimeout, func() {
		fired.Store(true)
		cancel()
	})
	return callCtx, cancel, func() { timer.Stop() }, fired.Load
}

// idleBody 包住流式响应体：后台定期看上游最近一次来数据的时间（antigravity.Response.LastRead，含解包时
// 丢掉的空事件和注释），超过 idle 就取消上游请求，之后的 Read 返回 ErrStreamIdle。关闭时停止检查。
type idleBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	fired  atomic.Bool
	stop   chan struct{}
	once   sync.Once
}

// watchIdle 给流式响应体加空闲检查；idle 为 0 时不检查，只在关闭时取消 ctx。
func watchIdle(up *antigravity.Response, idle time.Duration, cancel context.CancelFunc) io.ReadCloser {
	if idle <= 0 {
		return withCancel(up.Body, cancel)
	}
	b := &idleBody{ReadCloser: up.Body, cancel: cancel, stop: make(chan struct{})}
	tick := min(max(idle/4, 50*time.Millisecond), 5*time.Second)
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-b.stop:
				return
			case now := <-t.C:
				if now.Sub(up.LastRead()) >= idle {
					b.fired.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && b.fired.Load() {
		err = ErrStreamIdle
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.once.Do(func() { close(b.stop) })
	b.cancel()
	return b.ReadCloser.Close()
}

// streamIdled 报告流式响应体是否因为空闲被断开。
func streamIdled(body io.Reader) bool {
	b, ok := body.(*idleBody)
	return ok && b.fired.Load()
}
