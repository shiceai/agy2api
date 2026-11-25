package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Google 登录添加账号（流程见 antigravity/oauth.go）。授权会话只在内存里：有效 10 分钟、只能完成一次，
// 重启后作废。回调地址是随机端口的回环地址，浏览器跳过去打不开，用户把地址栏里的地址粘贴回来；
// 没有不鉴权的回调接口，start 和 complete 都走管理端鉴权。
const (
	oauthSessionTTL   = 10 * time.Minute
	oauthMaxSessions  = 20
	oauthCallbackPath = "/oauth-callback"
)

type oauthSession struct {
	state       string
	verifier    string
	redirectURI string
	expires     time.Time
}

type oauthSessions struct {
	mu sync.Mutex
	m  map[string]*oauthSession
}

// add 保存会话，顺带清掉过期的；会话太多时丢掉最早过期的。
func (o *oauthSessions) add(id string, sess *oauthSession) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.m == nil {
		o.m = map[string]*oauthSession{}
	}
	now := time.Now()
	for k, v := range o.m {
		if now.After(v.expires) {
			delete(o.m, k)
		}
	}
	for len(o.m) >= oauthMaxSessions {
		var oldest string
		for k, v := range o.m {
			if oldest == "" || v.expires.Before(o.m[oldest].expires) {
				oldest = k
			}
		}
		delete(o.m, oldest)
	}
	o.m[id] = sess
}

// get 返回未过期的会话。
func (o *oauthSessions) get(id string) (*oauthSession, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	sess, ok := o.m[id]
	if !ok || time.Now().After(sess.expires) {
		return nil, false
	}
	return sess, true
}

// take 取出并删除会话，同一个会话只能完成一次；并发的第二次拿不到。
func (o *oauthSessions) take(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.m[id]; !ok {
		return false
	}
	delete(o.m, id)
	return true
}

// randomToken 返回 n 字节随机数的 base64url 编码（不带填充）。
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// randomPort 返回动态端口范围（49152～65535）里的随机端口，只用来拼回调地址，不监听。
func randomPort() (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(65535-49152+1))
	if err != nil {
		return 0, err
	}
	return 49152 + int(n.Int64()), nil
}

// adminOAuthStart 开始一次 Google 登录，返回授权链接和会话 ID。
func (s *Service) adminOAuthStart(w http.ResponseWriter, r *http.Request) {
	idBytes := make([]byte, 16)
	state, err1 := randomToken(32)
	verifier, err2 := randomToken(32) // 43 个字符，PKCE 要求 43～128
	port, err3 := randomPort()
	_, err4 := rand.Read(idBytes)
	for _, err := range []error{err1, err2, err3, err4} {
		if err != nil {
			adminError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	id := hex.EncodeToString(idBytes)
	sess := &oauthSession{
		state:       state,
		verifier:    verifier,
		redirectURI: fmt.Sprintf("http://127.0.0.1:%d%s", port, oauthCallbackPath),
		expires:     time.Now().Add(oauthSessionTTL),
	}
	s.oauth.add(id, sess)
	// 打开添加账号弹窗就会生成，不代表真的去登录了，只记 debug；添加成功时导入逻辑会记 info。
	slog.DebugContext(r.Context(), "已生成授权链接")
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": id,
		"auth_url":   s.client.AuthorizationURL(sess.redirectURI, state, verifier),
		"expires_at": sess.expires.UnixMilli(),
	})
}

// callbackQuery 从粘贴的内容里取出回调参数：通常是浏览器地址栏里的完整地址，也接受只粘贴了问号后面的部分。
func callbackQuery(raw string) url.Values {
	raw = strings.TrimSpace(raw)
	if u, err := url.Parse(raw); err == nil && u.RawQuery != "" {
		return u.Query()
	}
	q, _ := url.ParseQuery(strings.TrimPrefix(raw, "?"))
	return q
}

// adminOAuthComplete 用粘贴回来的回调地址完成 Google 登录：核对 state，用授权码换令牌，再按导入流程添加账号。
// 回调地址和会话对不上时会话保留，可以重新粘贴；核对通过后会话即作废。
func (s *Service) adminOAuthComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID   string `json:"session_id"`
		CallbackURL string `json:"callback_url"`
		// Proxy 是新账号要绑定的代理，换授权码就经它出网；不传为直连。
		Proxy *proxyChoice `json:"proxy"`
	}
	if !decodeAdminJSON(w, r, 1<<16, &req) {
		return
	}
	sess, ok := s.oauth.get(req.SessionID)
	if !ok {
		adminError(w, http.StatusBadRequest, "授权链接已过期或已使用，请重新开始")
		return
	}
	q := callbackQuery(req.CallbackURL)
	if e := q.Get("error"); e != "" {
		s.oauth.take(req.SessionID)
		if e == "access_denied" {
			adminError(w, http.StatusBadRequest, "授权被取消（access_denied），请重新开始")
		} else {
			adminError(w, http.StatusBadRequest, "Google 返回错误："+e+"，请重新开始")
		}
		return
	}
	code := q.Get("code")
	if code == "" || q.Get("state") == "" {
		adminError(w, http.StatusBadRequest, "没有找到授权码，请粘贴浏览器地址栏里的完整地址")
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(sess.state)) != 1 {
		adminError(w, http.StatusBadRequest, "这个地址不是本次授权的回调，请确认粘贴的是刚才授权后的地址")
		return
	}
	if !s.oauth.take(req.SessionID) {
		adminError(w, http.StatusBadRequest, "授权链接已过期或已使用，请重新开始")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), importTimeout)
	defer cancel()
	proxy, err := s.resolveProxyChoice(ctx, req.Proxy)
	if err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return
	}
	proxyURL := ""
	if proxy != nil {
		proxyURL = proxy.URL
	}
	cred, err := s.client.ExchangeCode(ctx, proxyURL, code, sess.redirectURI, sess.verifier)
	if err != nil {
		slog.WarnContext(ctx, "Google 登录换取令牌失败", "err", err)
		adminError(w, http.StatusBadGateway, "换取令牌失败："+err.Error())
		return
	}
	res, err := s.importCredential(ctx, cred, proxy)
	if err != nil {
		slog.WarnContext(ctx, "Google 登录添加账号失败", "err", err)
		importError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
