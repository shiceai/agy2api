package antigravity

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// basePolicy 决定一次上游调用遇到什么情况换下一个基址（daily → prod）。
//
// 连接失败、408、5xx 一律换：这类问题常常只出在某个基址上。429 是否换由 retry429 决定：
// 数据面（生成、计数）不换，额度按账号算，换基址只会多打一次请求，应交给调用方冷却换号；
// 控制面（loadCodeAssist、onboardUser、fetchAvailableModels）换：调用量小，换基址重试的代价也小。
type basePolicy struct {
	retry429 bool
}

var (
	dataPlane    = basePolicy{}
	controlPlane = basePolicy{retry429: true}
)

func (p basePolicy) fallback(status int) bool {
	return status == http.StatusRequestTimeout || status >= 500 || (p.retry429 && status == http.StatusTooManyRequests)
}

// postWithFallback 按 upstreamBases 顺序向 {base}/v1internal:<method><query> 发 POST，
// headers 是每个请求都带的头。遇到 policy 规定要回退的情况换下一个基址；
// 返回第一个不需要回退的响应，调用方负责关闭。
// 所有基址都要回退时，拿到过响应就返回最后一个（错误体已读进内存，可重复读取），交给调用方分类；
// 一个响应都没拿到才返回 error。
func (c *Client) postWithFallback(ctx context.Context, hc *http.Client, policy basePolicy, method, query string, headers http.Header, payload []byte) (*http.Response, error) {
	var lastErr error
	var last *http.Response
	for _, base := range c.upstreamBases {
		endpoint := strings.TrimRight(base, "/") + "/v1internal:" + method + query
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header = headers.Clone()
		resp, err := hc.Do(req)
		if err != nil && ctx.Err() != nil {
			return nil, err // 请求已取消或超时，换基址也一样
		}
		if err != nil && IsProxyError(err) {
			return nil, err // 换基址还是经同一个代理，没用
		}
		if err != nil {
			slog.WarnContext(ctx, "上游基址请求失败，换下一个基址", "method", method, "base", base, "err", err)
			lastErr = err
			continue
		}
		if !policy.fallback(resp.StatusCode) {
			return resp, nil
		}
		slog.WarnContext(ctx, "上游基址返回可回退的状态，换下一个基址", "method", method, "base", base, "status", resp.StatusCode)
		// 读出错误体再关闭，连接可复用；最后一个响应要交给调用方分类。
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		last = resp
	}
	if last != nil {
		return last, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("upstream: no base configured")
	}
	return nil, lastErr
}

// cloudCodeHeaders 返回 Cloud Code 请求的公共头。不设 Accept：官方客户端实抓的请求头只有
// User-Agent、Authorization、Content-Type 和 Go 自动加的 Accept-Encoding。
func cloudCodeHeaders(accessToken, userAgent string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+accessToken)
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", userAgent)
	return h
}
