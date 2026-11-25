package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// CountTokens 调用上游 v1internal:countTokens，解包 response 后返回标准 Gemini JSON。
// 基址按数据面策略回退（429 不换基址）。payload 由 BuildCountTokensBody 生成。
// 返回非 nil error 表示上游失败，调用方可回退到本地估算。proxy 是账号绑定的代理（空为直连），
// userAgent 是该账号的 Cloud Code User-Agent，空时用默认值。
func (c *Client) CountTokens(ctx context.Context, proxy, accessToken, userAgent string, payload []byte) ([]byte, error) {
	rt, err := c.route(proxy)
	if err != nil {
		return nil, err
	}
	if userAgent == "" {
		userAgent = c.UserAgentFor("", "")
	}
	resp, err := c.postWithFallback(ctx, rt.data, dataPlane, "countTokens", "", cloudCodeHeaders(accessToken, userAgent), payload)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, responseBodyLimit))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("countTokens HTTP %d", resp.StatusCode)
	}
	return unwrapCountTokens(raw), nil
}

func unwrapCountTokens(raw []byte) []byte {
	var env map[string]any
	if json.Unmarshal(raw, &env) != nil {
		return raw
	}
	if r, ok := env["response"].(map[string]any); ok {
		if out, err := json.Marshal(r); err == nil {
			return out
		}
	}
	return raw
}

// LocalTokenEstimate 是上游不可用时的粗略本地估算（约每 4 字符 1 token）。
func LocalTokenEstimate(geminiReq map[string]any) int {
	chars := 0
	contents, _ := geminiReq["contents"].([]any)
	for _, c := range contents {
		m, _ := c.(map[string]any)
		parts, _ := m["parts"].([]any)
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if t, ok := pm["text"].(string); ok {
				chars += len(t)
			}
		}
	}
	if chars == 0 {
		return 1
	}
	return (chars + 3) / 4
}
