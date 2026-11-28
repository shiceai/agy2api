// Package notify 把值得管理员知道的事件（账号被封、refresh token 失效、没号可用、号池暂停等）发到
// 管理端配置的通知渠道（通用 Webhook、钉钉、飞书、企业微信、Telegram）。
//
// 发送在后台排队进行，不阻塞请求：同一事件键在限频时长内只发一次，连不上、5xx 时重试，每个渠道记下最近一次的结果。
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // 精简镜像里可能没有时区数据库，通知里的时间要按管理员的时区显示

	"github.com/shiceai/agy2api/internal/store"
)

// 事件类型，也是设置里各事件开关的键。
const (
	EventAccountBanned      = "account_banned"      // 账号被条款封禁
	EventTokenInvalid       = "token_invalid"       // refresh token 失效
	EventValidationRequired = "validation_required" // 账号需要验证
	EventNoAccount          = "no_account"          // 某模型没有可用账号
	EventPoolPause          = "pool_pause"          // 号池暂停开始、结束
	EventProxyFailed        = "proxy_failed"        // 代理后台测试失败
	EventNewVersion         = "new_version"         // 有新版本
	eventTest               = "test"                // 管理端的发送测试，不受开关限制
)

// defaultEvents 是各事件默认是否通知。
var defaultEvents = map[string]bool{
	EventAccountBanned:      true,
	EventTokenInvalid:       true,
	EventValidationRequired: true,
	EventNoAccount:          true,
	EventPoolPause:          true,
	EventProxyFailed:        false,
	EventNewVersion:         false,
}

// Settings 是通知设置：哪些事件要通知、实例名称、显示时间用的时区。
type Settings struct {
	Events map[string]bool `json:"events"`
	// Instance 是实例名称，写进每条通知，区分多套部署；空时不写。
	Instance string `json:"instance"`
	// Timezone 是 IANA 时区名（如 Asia/Shanghai），管理端保存时取浏览器的时区；空时用服务器的本地时区。
	Timezone string `json:"timezone"`
}

// DefaultSettings 是没配置时的设置。
func DefaultSettings() Settings {
	events := make(map[string]bool, len(defaultEvents))
	for k, v := range defaultEvents {
		events[k] = v
	}
	return Settings{Events: events}
}

// Validate 检查设置，错误说明直接给管理端看。
func (s Settings) Validate() error {
	for k := range s.Events {
		if _, ok := defaultEvents[k]; !ok {
			return fmt.Errorf("unknown event %q", k)
		}
	}
	if len([]rune(s.Instance)) > 40 {
		return errors.New("实例名称最多 40 个字")
	}
	if s.Timezone != "" {
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return fmt.Errorf("unknown timezone %q", s.Timezone)
		}
	}
	return nil
}

// enabled 报告事件是否要通知：设置里没有的按默认值。
func (s Settings) enabled(event string) bool {
	if event == eventTest {
		return true
	}
	if v, ok := s.Events[event]; ok {
		return v
	}
	return defaultEvents[event]
}

// Message 是一条要发出的通知。
type Message struct {
	Event  string
	Title  string // 如"账号被条款封禁"，渲染时前面加上产品名
	Fields []Field
	// Key 非空时限频：Throttle 内同一个 Key 只发一次（如同一账号、同一模型）。
	Key      string
	Throttle time.Duration
	// At 是事件发生的时间，零值时取入队时间。
	At time.Time
}

// Field 是通知里的一行"名称：值"。Time 非零时值按设置的时区格式化，Value 不用。
type Field struct {
	Label string
	Value string
	Time  time.Time
}

// rendered 是按设置渲染好的消息，各渠道共用。
type rendered struct {
	event, title, text, instance string
	fields                       []Field // Value 已填好
	at                           time.Time
}

// productName 写在每条通知的标题前，也方便钉钉机器人用关键词校验。
const productName = "AGY2API"

// render 把消息渲染成纯文本：标题、实例、各字段、时间各占一行。
func render(m Message, s Settings, loc *time.Location) rendered {
	r := rendered{event: m.Event, title: m.Title, instance: s.Instance, at: m.At}
	var b strings.Builder
	b.WriteString("【" + productName + "】" + m.Title)
	if s.Instance != "" {
		b.WriteString("\n实例：" + s.Instance)
	}
	for _, f := range m.Fields {
		if !f.Time.IsZero() {
			f.Value = formatTime(f.Time, loc)
		}
		if f.Value == "" {
			continue
		}
		r.fields = append(r.fields, Field{Label: f.Label, Value: f.Value})
		b.WriteString("\n" + f.Label + "：" + f.Value)
	}
	b.WriteString("\n时间：" + formatTime(m.At, loc))
	r.text = b.String()
	return r
}

// formatTime 格式化成"2006-01-02 15:04:05（UTC+08:00）"。
func formatTime(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2006-01-02 15:04:05（UTC-07:00）")
}

// MaskEmail 给邮箱打码：保留用户名第一个字符和域名，如 a***@gmail.com。通知会经过第三方服务器，不发完整邮箱。
func MaskEmail(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	local, domain, ok := strings.Cut(email, "@")
	first := []rune(local)
	head := ""
	if len(first) > 0 {
		head = string(first[0])
	}
	if !ok {
		return head + "***"
	}
	return head + "***@" + domain
}

// AccountLabel 是通知里账号的写法：有名称时"名称（打码邮箱）"，否则只写打码邮箱。名称就是邮箱时也打码。
func AccountLabel(name, email string) string {
	masked := MaskEmail(email)
	name = strings.TrimSpace(name)
	switch {
	case name == "" || strings.EqualFold(name, email):
		if masked == "" {
			return "（未知账号）"
		}
		return masked
	case masked == "":
		return name
	}
	return name + "（" + masked + "）"
}

// 发送的参数。
const (
	queueSize  = 64
	maxRetries = 2 // 连不上、5xx 时最多再试几次
)

// retryBackoff 是第 n 次重试前的等待（测试时替换）。
var retryBackoff = func(n int) time.Duration { return time.Duration(2+4*(n-1)) * time.Second }

// Dispatcher 管理通知设置、限频和后台发送。零值不可用，用 New 构造，用完调用 Close。
type Dispatcher struct {
	store  *store.Store
	enc    store.Encryptor
	client *http.Client

	mu       sync.Mutex
	settings Settings
	loc      *time.Location
	sentAt   map[string]time.Time // 限频：事件键上次入队的时间

	queue   chan Message
	stop    chan struct{}
	done    chan struct{}
	closing sync.Once
}

// New 构造并启动后台发送。发送直接从服务器出网（认 HTTPS_PROXY 等环境变量），不走账号绑定的代理。
func New(st *store.Store, enc store.Encryptor) *Dispatcher {
	d := &Dispatcher{
		store:    st,
		enc:      enc,
		client:   &http.Client{Timeout: sendTimeout},
		settings: DefaultSettings(),
		loc:      time.Local,
		sentAt:   map[string]time.Time{},
		queue:    make(chan Message, queueSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go d.run()
	return d
}

// SetSettings 替换通知设置，立即生效。时区不认识时用服务器的本地时区（保存时已校验过）。
func (d *Dispatcher) SetSettings(s Settings) {
	loc := time.Local
	if s.Timezone != "" {
		if l, err := time.LoadLocation(s.Timezone); err == nil {
			loc = l
		}
	}
	d.mu.Lock()
	d.settings, d.loc = s, loc
	d.mu.Unlock()
}

// Notify 把消息排进发送队列，不阻塞：事件没开、在限频时长内、队列满或已关闭时丢弃。返回是否入队。
func (d *Dispatcher) Notify(m Message) bool {
	if m.At.IsZero() {
		m.At = time.Now()
	}
	d.mu.Lock()
	if !d.settings.enabled(m.Event) {
		d.mu.Unlock()
		return false
	}
	if m.Key != "" && m.Throttle > 0 {
		if last, ok := d.sentAt[m.Key]; ok && m.At.Sub(last) < m.Throttle {
			d.mu.Unlock()
			return false
		}
		d.sentAt[m.Key] = m.At
		d.pruneLocked(m.At)
	}
	d.mu.Unlock()
	select {
	case <-d.stop:
		return false
	default:
	}
	select {
	case d.queue <- m:
		return true
	default:
		slog.Warn("通知队列已满，丢弃一条通知", "event", m.Event)
		return false
	}
}

// pruneLocked 删掉一天以前的限频记录，免得事件键（账号、模型）越积越多。需持 d.mu。
func (d *Dispatcher) pruneLocked(now time.Time) {
	if len(d.sentAt) < 256 {
		return
	}
	for k, t := range d.sentAt {
		if now.Sub(t) > 24*time.Hour {
			delete(d.sentAt, k)
		}
	}
}

// Test 立即给一个渠道发一条测试消息（不重试），记下结果并返回发送错误。
func (d *Dispatcher) Test(ctx context.Context, ch *store.NotifyChannel) error {
	m := Message{
		Event:  eventTest,
		Title:  "测试消息",
		Fields: []Field{{Label: "说明", Value: "收到这条消息，说明通知渠道配置正确。"}},
		At:     time.Now(),
	}
	return d.deliver(ctx, ch, d.render(m), 0)
}

// Close 停止接收新通知，等队列里的发完；ctx 到期则不再等。
func (d *Dispatcher) Close(ctx context.Context) error {
	d.closing.Do(func() { close(d.stop) })
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher) render(m Message) rendered {
	d.mu.Lock()
	s, loc := d.settings, d.loc
	d.mu.Unlock()
	return render(m, s, loc)
}

// run 逐条发送队列里的通知；关闭后把剩下的发完再退出。
func (d *Dispatcher) run() {
	defer close(d.done)
	for {
		select {
		case m := <-d.queue:
			d.dispatch(m)
		case <-d.stop:
			for {
				select {
				case m := <-d.queue:
					d.dispatch(m)
				default:
					return
				}
			}
		}
	}
}

// dispatch 把一条通知发到所有启用的渠道。
func (d *Dispatcher) dispatch(m Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	channels, err := d.store.ListNotifyChannels(ctx, d.enc)
	if err != nil {
		slog.Error("读取通知渠道失败，这条通知没发出去", "event", m.Event, "err", err)
		return
	}
	r := d.render(m)
	for _, ch := range channels {
		if ch.Enabled {
			_ = d.deliver(ctx, ch, r, maxRetries)
		}
	}
}

// sleepCtx 等 d，ctx 先结束时返回 false。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// deliver 发到一个渠道，可重试的失败最多再试 retries 次；记下结果和日志（不含地址、密钥）。
func (d *Dispatcher) deliver(ctx context.Context, ch *store.NotifyChannel, r rendered, retries int) error {
	cfg, err := ParseConfig(ch.Config)
	if err == nil {
		for attempt := 1; ; attempt++ {
			err = send(ctx, d.client, ch.Type, cfg, r, time.Now())
			var se *sendError
			if err == nil || attempt > retries || !errors.As(err, &se) || !se.retryable || !sleepCtx(ctx, retryBackoff(attempt)) {
				break
			}
		}
	}
	errText := ""
	if err != nil {
		errText = clip(err.Error(), 300)
		slog.Warn("通知发送失败", "channel_id", ch.ID, "channel", ch.Name, "type", ch.Type, "event", r.event, "err", errText)
	} else {
		slog.Info("已发送通知", "channel_id", ch.ID, "channel", ch.Name, "event", r.event)
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if serr := d.store.SetNotifyChannelResult(wctx, ch.ID, time.Now().UnixMilli(), errText); serr != nil {
		slog.Error("保存通知发送结果失败", "channel_id", ch.ID, "err", serr)
	}
	return err
}
