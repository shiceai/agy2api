package relay

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/logx"
)

// eventLog 收集执行层发出的告警事件。
type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) add(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

// of 返回某类事件。
func (l *eventLog) of(typ string) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Event
	for _, e := range l.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (e *env) collectEvents() *eventLog {
	l := &eventLog{}
	e.relay.SetEventHandler(l.add)
	return l
}

func (e *env) enableTOSPause(window time.Duration) {
	p := e.relay.Policy()
	p.TOSPause, p.TOSPauseWindow = true, window
	e.relay.SetPolicy(p)
}

// 开了封禁保护：账号因条款封禁被停用后整个号池暂停，生成和计数请求都返回 503（Retry-After 到暂停结束）、
// 不再发往上游；暂停状态落库，重启后读回；管理端提前恢复后照常服务。事件带上申诉地址。
func TestBanPausesPool(t *testing.T) {
	e := newEnv(t, "a", "b")
	events := e.collectEvents()
	e.enableTOSPause(time.Hour)
	e.up.script["a"] = []int{tosAppeal403}

	if _, _, _, err := e.generate("gemini-2.5-flash", false); relayError(t, err).Status != http.StatusServiceUnavailable {
		t.Fatalf("条款封禁应返回 503：%v", err)
	}
	pp := e.relay.PoolPause()
	if pp == nil || pp.AccountID != e.ids["a"] || time.Until(pp.Until) < 59*time.Minute {
		t.Fatalf("应暂停号池约 1 小时，触发账号 a：%+v", pp)
	}
	banned := events.of(EventAccountBanned)
	if len(banned) != 1 || banned[0].AccountID != e.ids["a"] || banned[0].Link != "https://forms.gle/appeal-example" {
		t.Fatalf("应发出带申诉地址的封禁事件：%+v", banned)
	}
	if paused := events.of(EventPoolPaused); len(paused) != 1 || !paused[0].Until.Equal(pp.Until) {
		t.Fatalf("应发出一次暂停事件：%+v", paused)
	}

	ctx := logx.WithRequestID(context.Background(), "req-paused")
	_, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: map[string]any{}})
	re := relayError(t, err)
	if re.Status != http.StatusServiceUnavailable || !strings.Contains(re.Message, "paused") || re.RetryAfter < 59*time.Minute || re.RetryAfter > time.Hour+time.Second {
		t.Fatalf("暂停中应返回 503 和到暂停结束的 Retry-After：%+v", re)
	}
	if _, err := e.relay.CountTokens(context.Background(), "gemini-2.5-flash", map[string]any{}); relayError(t, err).Status != http.StatusServiceUnavailable {
		t.Fatalf("暂停中计数请求也应拒绝：%v", err)
	}
	if _, n := e.up.calls("b"); n != 0 {
		t.Fatalf("暂停中不应调用其他账号，b 被调用 %d 次", n)
	}

	// 重启后读回暂停。
	other := New(e.relay.client, e.store, e.enc)
	if err := other.LoadPoolPause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := other.PoolPause(); got == nil || !got.Until.Equal(pp.Until.Truncate(time.Millisecond)) || got.AccountID != e.ids["a"] {
		t.Fatalf("重启后应读回暂停：%+v，原来 %+v", got, pp)
	}
	other.Close(context.Background())

	if !e.relay.ResumePool(context.Background()) || e.relay.PoolPause() != nil {
		t.Fatal("提前恢复后不应再暂停")
	}
	if resumed := events.of(EventPoolResumed); len(resumed) != 1 || !resumed[0].Manual {
		t.Fatalf("应发出一次手动恢复事件：%+v", resumed)
	}
	if e.relay.ResumePool(context.Background()) {
		t.Fatal("没在暂停时恢复应返回 false")
	}
	if got := e.servedBy("gemini-2.5-flash", 2); got["b"] != 2 {
		t.Fatalf("恢复后应由 b 服务：%v", got)
	}
	again := New(e.relay.client, e.store, e.enc)
	if err := again.LoadPoolPause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again.PoolPause() != nil {
		t.Fatal("恢复后库里的暂停应已清掉")
	}
	again.Close(context.Background())

	rec, d := e.usageByRequest("req-paused")
	if rec.StatusCode != http.StatusServiceUnavailable || d.Error == nil || d.Error.Kind != KindPoolPaused {
		t.Fatalf("暂停中的请求应记一条号池暂停的用量：%d %+v", rec.StatusCode, d.Error)
	}
}

// 没开封禁保护：只停用被封的账号、发封禁事件，号池照常服务。
func TestBanWithoutPause(t *testing.T) {
	e := newEnv(t, "a", "b")
	events := e.collectEvents()
	e.up.script["a"] = []int{tos403}
	_, _, _, _ = e.generate("gemini-2.5-flash", false)
	if e.relay.PoolPause() != nil {
		t.Fatal("没开封禁保护不应暂停")
	}
	if banned := events.of(EventAccountBanned); len(banned) != 1 || banned[0].Link != "" {
		t.Fatalf("应发出不带申诉地址的封禁事件：%+v", banned)
	}
	if len(events.of(EventPoolPaused)) != 0 {
		t.Fatal("不应发出暂停事件")
	}
	if got := e.servedBy("gemini-2.5-flash", 2); got["b"] != 2 {
		t.Fatalf("应由 b 照常服务：%v", got)
	}
}

// 暂停中又有账号被封：延长到较晚的截止时间，不重复发暂停事件；到时自动结束并发出恢复事件。
func TestPoolPauseExtendsAndExpires(t *testing.T) {
	e := newEnv(t, "a", "b")
	events := e.collectEvents()
	e.enableTOSPause(300 * time.Millisecond)
	ctx := context.Background()
	e.relay.disableAccount(ctx, e.ids["a"], ReasonTOSViolation, "")
	first := e.relay.PoolPause()
	if first == nil {
		t.Fatal("应暂停号池")
	}
	time.Sleep(100 * time.Millisecond)
	e.relay.disableAccount(ctx, e.ids["b"], ReasonTOSViolation, "")
	second := e.relay.PoolPause()
	if second == nil || !second.Until.After(first.Until) || !second.Since.Equal(first.Since) || second.AccountID != e.ids["a"] {
		t.Fatalf("应延长暂停、保留开始时间和触发账号：%+v → %+v", first, second)
	}
	if n := len(events.of(EventPoolPaused)); n != 1 {
		t.Fatalf("延长不应重复发暂停事件，发了 %d 次", n)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(events.of(EventPoolResumed)) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	resumed := events.of(EventPoolResumed)
	if len(resumed) != 1 || resumed[0].Manual || e.relay.PoolPause() != nil {
		t.Fatalf("到时应自动结束并发出一次恢复事件：%+v", resumed)
	}
	if time.Now().Before(second.Until) {
		t.Fatal("不应在延长后的截止时间之前结束")
	}
}

// 告警事件：refresh token 失效、需要验证（带验证地址）、没号可用（带最早恢复时间）。
func TestAlertEvents(t *testing.T) {
	t.Run("refresh token 失效", func(t *testing.T) {
		e := newEnv(t, "a", "b")
		events := e.collectEvents()
		e.up.revoked["a"] = true
		_ = e.servedBy("gemini-2.5-flash", 2)
		if got := events.of(EventTokenInvalid); len(got) == 0 || got[0].AccountID != e.ids["a"] {
			t.Fatalf("应发出令牌失效事件：%+v", got)
		}
	})
	t.Run("需要验证", func(t *testing.T) {
		e := newEnv(t, "a")
		events := e.collectEvents()
		e.up.script["a"] = []int{validationURL403}
		_, _, _, _ = e.generate("gemini-2.5-flash", false)
		got := events.of(EventValidationRequired)
		if len(got) != 1 || got[0].AccountID != e.ids["a"] || got[0].Model != "gemini-2.5-flash" || got[0].Link != "https://accounts.google.com/signin/continue" {
			t.Fatalf("应发出带验证地址的需要验证事件：%+v", got)
		}
	})
	t.Run("没号可用", func(t *testing.T) {
		e := newEnv(t, "a", "b")
		events := e.collectEvents()
		e.up.script["a"] = []int{http.StatusTooManyRequests}
		e.up.script["b"] = []int{http.StatusTooManyRequests}
		if _, _, _, err := e.generate("gemini-2.5-flash", false); relayError(t, err).Status != http.StatusTooManyRequests {
			t.Fatalf("都额度耗尽应返回 429：%v", err)
		}
		got := events.of(EventNoAccount)
		if len(got) != 1 || got[0].Model != "gemini-2.5-flash" || time.Until(got[0].RetryAt) < 80*time.Second {
			t.Fatalf("应发出带最早恢复时间的没号可用事件：%+v", got)
		}
	})
}
