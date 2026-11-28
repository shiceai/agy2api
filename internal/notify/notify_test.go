package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/cryptox"
	"github.com/shiceai/agy2api/internal/store"
)

func TestRender(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	at := time.Date(2026, 11, 27, 10, 52, 3, 0, time.UTC)
	m := Message{Event: EventNoAccount, Title: "没有可用账号", At: at, Fields: []Field{
		{Label: "模型", Value: "gemini-3.5-flash"},
		{Label: "最早恢复", Time: at.Add(90 * time.Second)},
		{Label: "申诉", Value: ""}, // 空值不写
	}}
	r := render(m, Settings{Instance: "生产"}, loc)
	want := "【AGY2API】没有可用账号\n实例：生产\n模型：gemini-3.5-flash\n最早恢复：2026-11-27 18:53:33（UTC+08:00）\n时间：2026-11-27 18:52:03（UTC+08:00）"
	if r.text != want {
		t.Fatalf("渲染结果不对：\n%s\n期望：\n%s", r.text, want)
	}
	if len(r.fields) != 2 || r.fields[1].Value != "2026-11-27 18:53:33（UTC+08:00）" {
		t.Fatalf("字段应去掉空值并格式化时间：%+v", r.fields)
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"alice@gmail.com": "a***@gmail.com",
		"张三@example.cn":   "张***@example.cn",
		"noat":            "n***",
		"":                "",
	}
	for in, want := range cases {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q，期望 %q", in, got, want)
		}
	}
	if got := AccountLabel("工作号", "bob@gmail.com"); got != "工作号（b***@gmail.com）" {
		t.Errorf("有名称时应写名称加打码邮箱：%q", got)
	}
	if got := AccountLabel("bob@gmail.com", "bob@gmail.com"); got != "b***@gmail.com" {
		t.Errorf("名称就是邮箱时只写打码邮箱：%q", got)
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		typ string
		cfg Config
		ok  bool
	}{
		{TypeWebhook, Config{URL: "https://example.com/hook"}, true},
		{TypeDingTalk, Config{URL: "https://oapi.dingtalk.com/robot/send?access_token=x", Secret: "SEC1"}, true},
		{TypeFeishu, Config{URL: "ftp://example.com"}, false},
		{TypeWeCom, Config{}, false},
		{TypeTelegram, Config{Token: "123456789:AAbbCCddEEffGGhh_ii-jj", ChatID: "-1001234567890"}, true},
		{TypeTelegram, Config{Token: "bad", ChatID: "1"}, false},
		{TypeTelegram, Config{Token: "123456789:AAbbCCddEEffGGhh_ii-jj"}, false},
		{"slack", Config{URL: "https://example.com"}, false},
	}
	for _, c := range cases {
		if err := c.cfg.Validate(c.typ); (err == nil) != c.ok {
			t.Errorf("%s %+v：err=%v，期望通过=%v", c.typ, c.cfg, err, c.ok)
		}
	}
	if h := (Config{URL: "https://oapi.dingtalk.com/robot/send?access_token=abcdef1234"}).Hint(TypeDingTalk); h != "oapi.dingtalk.com/…1234" {
		t.Errorf("Hint 应只给主机名和末 4 位：%q", h)
	}
}

// capture 是记录收到的请求、按脚本回响应的假渠道。
type capture struct {
	mu       sync.Mutex
	paths    []string
	queries  []string
	bodies   []map[string]any
	statuses []int  // 依次返回的状态码，用完后 200
	reply    string // 200 时的响应体
}

func (c *capture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.queries = append(c.queries, r.URL.RawQuery)
	c.bodies = append(c.bodies, body)
	status := http.StatusOK
	if len(c.statuses) > 0 {
		status, c.statuses = c.statuses[0], c.statuses[1:]
	}
	reply := c.reply
	c.mu.Unlock()
	w.WriteHeader(status)
	_, _ = io.WriteString(w, reply)
}

func newCapture(t *testing.T, reply string) (*capture, *httptest.Server) {
	c := &capture{reply: reply}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	return c, srv
}

func sample() rendered {
	return render(Message{Event: EventAccountBanned, Title: "账号被条款封禁", At: time.UnixMilli(1764240723000),
		Fields: []Field{{Label: "账号", Value: "a***@gmail.com"}}}, Settings{}, time.UTC)
}

func TestSendFormats(t *testing.T) {
	now := time.UnixMilli(1764240723456)
	ctx := context.Background()
	client := http.DefaultClient

	t.Run("通用 Webhook", func(t *testing.T) {
		c, srv := newCapture(t, "")
		if err := send(ctx, client, TypeWebhook, Config{URL: srv.URL + "/hook"}, sample(), now); err != nil {
			t.Fatal(err)
		}
		b := c.bodies[0]
		if b["event"] != EventAccountBanned || b["title"] != "账号被条款封禁" || !strings.HasPrefix(b["text"].(string), "【AGY2API】") ||
			b["time"].(float64) != 1764240723000 || len(b["fields"].([]any)) != 1 {
			t.Fatalf("通用 Webhook 的 JSON 不对：%v", b)
		}
	})
	t.Run("钉钉加签", func(t *testing.T) {
		c, srv := newCapture(t, `{"errcode":0,"errmsg":"ok"}`)
		if err := send(ctx, client, TypeDingTalk, Config{URL: srv.URL + "/robot/send?access_token=tok", Secret: "SECabc"}, sample(), now); err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, []byte("SECabc"))
		mac.Write([]byte("1764240723456\nSECabc"))
		wantSign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		q := c.queries[0]
		if !strings.Contains(q, "access_token=tok&timestamp=1764240723456&sign=") || !strings.Contains(q, "sign="+url.QueryEscape(wantSign)) {
			t.Fatalf("钉钉签名参数不对：%s（期望签名 %s）", q, wantSign)
		}
		if c.bodies[0]["msgtype"] != "text" || !strings.Contains(c.bodies[0]["text"].(map[string]any)["content"].(string), "账号被条款封禁") {
			t.Fatalf("钉钉消息体不对：%v", c.bodies[0])
		}
	})
	t.Run("钉钉返回错误码", func(t *testing.T) {
		_, srv := newCapture(t, `{"errcode":310000,"errmsg":"keywords not in content"}`)
		err := send(ctx, client, TypeDingTalk, Config{URL: srv.URL + "/robot/send?access_token=tok"}, sample(), now)
		if err == nil || !strings.Contains(err.Error(), "310000") || !strings.Contains(err.Error(), "keywords") {
			t.Fatalf("应报出钉钉的错误码：%v", err)
		}
	})
	t.Run("飞书签名", func(t *testing.T) {
		c, srv := newCapture(t, `{"code":0,"msg":"success"}`)
		if err := send(ctx, client, TypeFeishu, Config{URL: srv.URL + "/open-apis/bot/v2/hook/x", Secret: "fs"}, sample(), now); err != nil {
			t.Fatal(err)
		}
		b := c.bodies[0]
		mac := hmac.New(sha256.New, []byte("1764240723\nfs"))
		if b["msg_type"] != "text" || b["timestamp"] != "1764240723" || b["sign"] != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
			t.Fatalf("飞书消息体或签名不对：%v", b)
		}
	})
	t.Run("飞书返回错误", func(t *testing.T) {
		_, srv := newCapture(t, `{"code":19021,"msg":"sign match fail"}`)
		if err := send(ctx, client, TypeFeishu, Config{URL: srv.URL}, sample(), now); err == nil || !strings.Contains(err.Error(), "19021") {
			t.Fatalf("应报出飞书的错误：%v", err)
		}
	})
	t.Run("企业微信", func(t *testing.T) {
		c, srv := newCapture(t, `{"errcode":0,"errmsg":"ok"}`)
		if err := send(ctx, client, TypeWeCom, Config{URL: srv.URL + "/cgi-bin/webhook/send?key=k"}, sample(), now); err != nil {
			t.Fatal(err)
		}
		if c.bodies[0]["msgtype"] != "text" {
			t.Fatalf("企业微信消息体不对：%v", c.bodies[0])
		}
	})
	t.Run("Telegram", func(t *testing.T) {
		c, srv := newCapture(t, `{"ok":true}`)
		old := telegramAPI
		telegramAPI = srv.URL
		t.Cleanup(func() { telegramAPI = old })
		if err := send(ctx, client, TypeTelegram, Config{Token: "123:abc", ChatID: "-100"}, sample(), now); err != nil {
			t.Fatal(err)
		}
		if c.paths[0] != "/bot123:abc/sendMessage" || c.bodies[0]["chat_id"] != "-100" || c.bodies[0]["disable_web_page_preview"] != true {
			t.Fatalf("Telegram 请求不对：%s %v", c.paths[0], c.bodies[0])
		}
	})
	t.Run("连不上时说明里不带地址", func(t *testing.T) {
		err := send(ctx, client, TypeWeCom, Config{URL: "http://127.0.0.1:1/cgi-bin/webhook/send?key=secret-key"}, sample(), now)
		var se *sendError
		if err == nil || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "127.0.0.1:1/") {
			t.Fatalf("错误说明不应带地址：%v", err)
		}
		if !errors.As(err, &se) || !se.retryable {
			t.Fatalf("连不上应可重试：%v", err)
		}
	})
}

func newDispatcher(t *testing.T) (*Dispatcher, *store.Store, store.Encryptor) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	enc, err := cryptox.New([]byte("test-key-material"))
	if err != nil {
		t.Fatal(err)
	}
	d := New(st, enc)
	t.Cleanup(func() { d.Close(context.Background()) })
	return d, st, enc
}

func addChannel(t *testing.T, st *store.Store, enc store.Encryptor, typ string, cfg Config, enabled bool) *store.NotifyChannel {
	t.Helper()
	c := &store.NotifyChannel{Name: typ, Type: typ, Enabled: enabled, Config: cfg.String()}
	if err := st.CreateNotifyChannel(context.Background(), enc, c); err != nil {
		t.Fatal(err)
	}
	return c
}

// 发送队列：事件开关、限频；发到启用的渠道，5xx 重试后成功，记下结果；停用的渠道不发。
func TestDispatcher(t *testing.T) {
	old := retryBackoff
	retryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { retryBackoff = old })

	d, st, enc := newDispatcher(t)
	c, srv := newCapture(t, "")
	c.statuses = []int{http.StatusBadGateway, http.StatusBadGateway} // 前两次 502，第三次成功
	on := addChannel(t, st, enc, TypeWebhook, Config{URL: srv.URL + "/on"}, true)
	addChannel(t, st, enc, TypeWebhook, Config{URL: srv.URL + "/off"}, false)

	msg := Message{Event: EventAccountBanned, Title: "账号被条款封禁", Key: "k", Throttle: time.Hour}
	if !d.Notify(msg) {
		t.Fatal("默认开着的事件应入队")
	}
	if d.Notify(msg) {
		t.Fatal("限频时长内同一个键不应再入队")
	}
	if d.Notify(Message{Event: EventProxyFailed, Title: "代理测试失败"}) {
		t.Fatal("默认关着的事件不应入队")
	}
	d.SetSettings(Settings{Events: map[string]bool{EventAccountBanned: false}})
	if d.Notify(Message{Event: EventAccountBanned, Title: "x", Key: "other"}) {
		t.Fatal("关掉的事件不应入队")
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	paths := append([]string(nil), c.paths...)
	c.mu.Unlock()
	if len(paths) != 3 || paths[2] != "/on" {
		t.Fatalf("应向启用的渠道发 3 次（重试 2 次），停用的不发：%v", paths)
	}
	got, err := st.GetNotifyChannel(context.Background(), enc, on.ID)
	if err != nil || got.LastSentAt == 0 || got.LastError != "" {
		t.Fatalf("应记下成功的结果：%+v %v", got, err)
	}
}

// 发送测试：不受事件开关限制、不重试，失败时记下原因并返回。
func TestDispatcherTest(t *testing.T) {
	d, st, enc := newDispatcher(t)
	d.SetSettings(Settings{Events: map[string]bool{}})
	c, srv := newCapture(t, `{"errcode":93000,"errmsg":"invalid webhook url"}`)
	ch := addChannel(t, st, enc, TypeWeCom, Config{URL: srv.URL + "/send?key=k"}, false)
	err := d.Test(context.Background(), ch)
	if err == nil || !strings.Contains(err.Error(), "93000") {
		t.Fatalf("应返回企业微信的错误：%v", err)
	}
	if len(c.paths) != 1 || !strings.Contains(c.bodies[0]["text"].(map[string]any)["content"].(string), "测试消息") {
		t.Fatalf("应发一次测试消息：%v", c.bodies)
	}
	got, _ := st.GetNotifyChannel(context.Background(), enc, ch.ID)
	if !strings.Contains(got.LastError, "93000") {
		t.Fatalf("应记下失败原因：%q", got.LastError)
	}
}
