package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 渠道类型。
const (
	TypeWebhook  = "webhook"  // 通用 Webhook：POST JSON（字段见 webhookPayload）
	TypeDingTalk = "dingtalk" // 钉钉自定义机器人，可选加签
	TypeFeishu   = "feishu"   // 飞书自定义机器人，可选签名校验
	TypeWeCom    = "wecom"    // 企业微信群机器人
	TypeTelegram = "telegram" // Telegram Bot：Bot Token + Chat ID
)

// ValidType 报告是不是支持的渠道类型。
func ValidType(t string) bool {
	switch t {
	case TypeWebhook, TypeDingTalk, TypeFeishu, TypeWeCom, TypeTelegram:
		return true
	}
	return false
}

// Config 是一个渠道的配置，整体加密存库（store.NotifyChannel.Config）。URL、Secret、Token 是凭据，
// 不写日志、不原样返回给接口（见 Hint）。
type Config struct {
	// URL 是 Webhook 地址（Telegram 不用）。
	URL string `json:"url,omitempty"`
	// Secret 是钉钉的加签密钥、飞书的签名校验密钥，可空（机器人没开加签/签名校验时）。
	Secret string `json:"secret,omitempty"`
	// Token、ChatID 是 Telegram 的 Bot Token 和接收消息的 Chat ID（用户、群组或频道）。
	Token  string `json:"token,omitempty"`
	ChatID string `json:"chat_id,omitempty"`
}

// ParseConfig 解析存库的配置 JSON。
func ParseConfig(raw string) (Config, error) {
	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Config{}, fmt.Errorf("渠道配置不是合法 JSON：%w", err)
	}
	return c, nil
}

// String 返回存库的 JSON。
func (c Config) String() string {
	b, _ := json.Marshal(c)
	return string(b)
}

var telegramTokenRE = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{10,100}$`)

// Validate 按渠道类型检查配置，错误说明直接给管理端看。
func (c Config) Validate(typ string) error {
	switch typ {
	case TypeWebhook, TypeDingTalk, TypeFeishu, TypeWeCom:
		if c.URL == "" {
			return errors.New("请填写 Webhook 地址")
		}
		if len(c.URL) > 2048 {
			return errors.New("地址太长，Webhook 地址最多 2048 个字符")
		}
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("地址应以 http:// 或 https:// 开头")
		}
		if len(c.Secret) > 256 {
			return errors.New("密钥太长")
		}
	case TypeTelegram:
		if !telegramTokenRE.MatchString(c.Token) {
			return errors.New("格式不对，Bot Token 应形如 123456789:AA…")
		}
		if c.ChatID == "" || len(c.ChatID) > 64 || strings.ContainsAny(c.ChatID, " \t\r\n") {
			return errors.New("请填写 Chat ID（数字 ID 或 @频道名）")
		}
	default:
		return fmt.Errorf("不支持的渠道类型 %q", typ)
	}
	return nil
}

// Hint 返回能在管理端显示的目标说明，不含凭据：Webhook 类显示主机名和地址末 4 位，Telegram 显示 Chat ID。
func (c Config) Hint(typ string) string {
	if typ == TypeTelegram {
		return "Chat " + c.ChatID
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" {
		return ""
	}
	tail := c.URL
	if n := len(tail); n > 4 {
		tail = tail[n-4:]
	}
	return u.Host + "/…" + tail
}

// sendTimeout 是一次发送（含连接）的时限。
const sendTimeout = 10 * time.Second

// responseLimit 是读渠道响应体的上限。
const responseLimit = 64 << 10

// telegramAPI 是 Telegram Bot API 的地址，测试时替换。
var telegramAPI = "https://api.telegram.org"

// webhookPayload 是通用 Webhook 收到的 JSON。
type webhookPayload struct {
	Event    string         `json:"event"`
	Title    string         `json:"title"`
	Text     string         `json:"text"`
	Instance string         `json:"instance,omitempty"`
	Fields   []webhookField `json:"fields"`
	Time     int64          `json:"time"` // Unix 毫秒
}

type webhookField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// sendError 是一次发送的失败；retryable 为真时（连不上、超时、5xx、429）值得重试，
// 配置问题（4xx、机器人返回错误码）重试也一样，不重试。
type sendError struct {
	msg       string
	retryable bool
}

func (e *sendError) Error() string { return e.msg }

// send 把渲染好的消息发到一个渠道。now 用于加签的时间戳。
func send(ctx context.Context, client *http.Client, typ string, c Config, m rendered, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	switch typ {
	case TypeWebhook:
		fields := make([]webhookField, 0, len(m.fields))
		for _, f := range m.fields {
			fields = append(fields, webhookField{f.Label, f.Value})
		}
		_, err := postJSON(ctx, client, c.URL, webhookPayload{
			Event: m.event, Title: m.title, Text: m.text, Instance: m.instance, Fields: fields, Time: m.at.UnixMilli(),
		})
		return err
	case TypeDingTalk:
		target := c.URL
		if c.Secret != "" {
			target = dingTalkSigned(c.URL, c.Secret, now)
		}
		body, err := postJSON(ctx, client, target, map[string]any{"msgtype": "text", "text": map[string]string{"content": m.text}})
		if err != nil {
			return err
		}
		return checkErrcode("钉钉", body)
	case TypeFeishu:
		payload := map[string]any{"msg_type": "text", "content": map[string]string{"text": m.text}}
		if c.Secret != "" {
			ts := strconv.FormatInt(now.Unix(), 10)
			payload["timestamp"], payload["sign"] = ts, feishuSign(ts, c.Secret)
		}
		body, err := postJSON(ctx, client, c.URL, payload)
		if err != nil {
			return err
		}
		return checkFeishu(body)
	case TypeWeCom:
		body, err := postJSON(ctx, client, c.URL, map[string]any{"msgtype": "text", "text": map[string]string{"content": m.text}})
		if err != nil {
			return err
		}
		return checkErrcode("企业微信", body)
	case TypeTelegram:
		body, err := postJSON(ctx, client, telegramAPI+"/bot"+c.Token+"/sendMessage",
			map[string]any{"chat_id": c.ChatID, "text": m.text, "disable_web_page_preview": true})
		if err != nil {
			return err
		}
		var r struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
		}
		if json.Unmarshal(body, &r) != nil || !r.OK {
			return &sendError{msg: "Telegram 返回失败：" + clip(r.Description, 200)}
		}
		return nil
	}
	return &sendError{msg: "不支持的渠道类型 " + typ}
}

// postJSON 发 POST 请求，2xx 时返回响应体。错误说明不含地址（地址里有凭据）。
func postJSON(ctx context.Context, client *http.Client, target string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, &sendError{msg: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return nil, &sendError{msg: "Webhook 地址不对"}
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, &sendError{msg: "连不上：" + transportReason(ctx, err), retryable: true}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, responseLimit))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := "HTTP " + strconv.Itoa(resp.StatusCode)
		if s := clip(strings.TrimSpace(string(body)), 200); s != "" {
			msg += "：" + s
		}
		return nil, &sendError{msg: msg, retryable: resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests}
	}
	return body, nil
}

// transportReason 去掉 *url.Error 里的地址（Go 的错误会带上完整 URL，里面有凭据），只留原因。
func transportReason(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "超时"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return clip(err.Error(), 200)
}

// checkErrcode 检查钉钉、企业微信的响应：{"errcode":0,"errmsg":"ok"}。
func checkErrcode(name string, body []byte) error {
	var r struct {
		Errcode *int   `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}
	if json.Unmarshal(body, &r) != nil || r.Errcode == nil {
		return &sendError{msg: name + "返回了无法识别的响应"}
	}
	if *r.Errcode != 0 {
		return &sendError{msg: fmt.Sprintf("%s返回错误 %d：%s", name, *r.Errcode, clip(r.Errmsg, 200))}
	}
	return nil
}

// checkFeishu 检查飞书的响应：{"code":0,"msg":"success"}，旧版接口是 {"StatusCode":0}。
func checkFeishu(body []byte) error {
	var r struct {
		Code       *int   `json:"code"`
		Msg        string `json:"msg"`
		StatusCode *int   `json:"StatusCode"`
	}
	if json.Unmarshal(body, &r) != nil {
		return &sendError{msg: "飞书返回了无法识别的响应"}
	}
	switch {
	case r.Code != nil && *r.Code != 0:
		return &sendError{msg: fmt.Sprintf("飞书返回错误 %d：%s", *r.Code, clip(r.Msg, 200))}
	case r.Code == nil && (r.StatusCode == nil || *r.StatusCode != 0):
		return &sendError{msg: "飞书返回了无法识别的响应"}
	}
	return nil
}

// dingTalkSigned 给钉钉 Webhook 地址加上签名参数：sign = Base64(HMAC-SHA256(secret, timestamp+"\n"+secret))，
// timestamp 是毫秒。
func dingTalkSigned(target, secret string, now time.Time) string {
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	return target + sep + "timestamp=" + ts + "&sign=" + url.QueryEscape(sign)
}

// feishuSign 是飞书的签名：Base64(HMAC-SHA256(key = timestamp+"\n"+secret, 空消息))，timestamp 是秒。
func feishuSign(ts, secret string) string {
	mac := hmac.New(sha256.New, []byte(ts+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// clip 把字符串截到 n 个字符。
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
