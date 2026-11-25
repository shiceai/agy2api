package antigravity

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 远程图片内联：客户端把图片以 http(s) URL 放在 fileData.fileUri 里时，上游（Gemini/Antigravity）不接受
// 任意 http URL，只认内联数据或它自己托管的文件。这里把这些 URL 下载下来、转成 inlineData 内联到请求里。
//
// 下载客户端给的 URL 有 SSRF 风险（可能指向内网/云元数据地址），所以严格限制：只连解析出的公网 IP、
// 按 IP 直连并保留域名做 SNI、限制数量/大小/重定向，默认关闭，由管理端开启。

const (
	maxRemoteImages     = 8
	maxRemoteImageBytes = 10 << 20 // 一次请求所有图片合计上限
	remoteImageTimeout  = 20 * time.Second
	maxImageRedirects   = 4
)

// geminiFileURI 匹配上游自己托管的文件（这类 URL 上游直接认，不下载）。
var geminiFileURI = regexp.MustCompile(`^https://generativelanguage\.googleapis\.com/v1(?:beta|alpha)?/files/[A-Za-z0-9_-]+`)

// RemoteImageError 是下载图片失败的客户端可见错误，只带通用说明，不含 URL 或响应内容。
type RemoteImageError struct{ msg string }

func (e *RemoteImageError) Error() string { return e.msg }

func imageErr(msg string) *RemoteImageError { return &RemoteImageError{msg: msg} }

// imageTarget 是一个待内联的图片位置。
type imageTarget struct {
	content int
	part    int
	url     string
}

// InlineRemoteImages 把请求体里 http(s) 图片 URL 下载后内联成 inlineData，返回内联的张数。原地改 body。
// 失败返回 *RemoteImageError（客户端可见、不泄露 URL/内容）。
func InlineRemoteImages(ctx context.Context, body map[string]any) (int, error) {
	targets := collectImageTargets(body)
	if len(targets) == 0 {
		return 0, nil
	}
	if len(targets) > maxRemoteImages {
		return 0, imageErr(fmt.Sprintf("at most %d remote images are allowed per request", maxRemoteImages))
	}
	ctx, cancel := context.WithTimeout(ctx, remoteImageTimeout)
	defer cancel()
	client := imageClientFactory()
	remaining := int64(maxRemoteImageBytes)
	contents, _ := body["contents"].([]any)
	for _, t := range targets {
		inline, n, err := downloadImage(ctx, client, t.url, remaining)
		if err != nil {
			return 0, err
		}
		remaining -= n
		part := contents[t.content].(map[string]any)["parts"].([]any)[t.part].(map[string]any)
		delete(part, "fileData")
		delete(part, "file_data")
		part["inlineData"] = inline
	}
	return len(targets), nil
}

// collectImageTargets 找出所有 fileData 为 image/* 且 fileUri 是 http(s)（非上游托管文件）的 part。
func collectImageTargets(body map[string]any) []imageTarget {
	contents, ok := body["contents"].([]any)
	if !ok {
		return nil
	}
	var targets []imageTarget
	for ci, c := range contents {
		content, ok := c.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := content["parts"].([]any)
		if !ok {
			continue
		}
		for pi, p := range parts {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			fd, ok := part["fileData"].(map[string]any)
			if !ok {
				fd, ok = part["file_data"].(map[string]any)
			}
			if !ok {
				continue
			}
			mime, _ := firstString(fd, "mimeType", "mime_type")
			url, _ := firstString(fd, "fileUri", "file_uri")
			if !strings.HasPrefix(strings.ToLower(mime), "image/") {
				continue
			}
			if !isHTTPURL(url) || geminiFileURI.MatchString(url) {
				continue
			}
			targets = append(targets, imageTarget{content: ci, part: pi, url: url})
		}
	}
	return targets
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			return s, true
		}
	}
	return "", false
}

func isHTTPURL(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// imageClientFactory 构造下载图片用的 HTTP 客户端；测试里可替换成不带 SSRF 限制、指向本地假服务器的客户端。
var imageClientFactory = newImageClient

// newImageClient 构造只连公网 IP 的 HTTP 客户端：自定义拨号在解析后校验 IP，并按 IP 直连（避免 DNS 重绑定）。
func newImageClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return safeDial(ctx, dialer, network, addr)
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   remoteImageTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxImageRedirects {
				return imageErr("image URL returned too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return imageErr("image URL redirected to a non-HTTP scheme")
			}
			return nil
		},
	}
}

// safeDial 解析目标域名，要求所有解析出的 IP 都是公网地址，再按 IP 直连（TLS SNI 仍用原域名，由 Transport 处理）。
func safeDial(ctx context.Context, dialer *net.Dialer, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, imageErr("invalid image host")
	}
	if port != "80" && port != "443" {
		return nil, imageErr("image URL must use the default HTTP or HTTPS port")
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, imageErr("could not resolve the image host")
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, imageErr("image URL must resolve to a public IP address")
		}
	}
	var lastErr error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// isPublicIP 判断是否公网单播地址：排除环回、私网、链路本地、组播、CGNAT、文档/基准测试等保留段。
func isPublicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127: // CGNAT 100.64.0.0/10
			return false
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0: // 192.0.0.0/24
			return false
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 2: // 192.0.2.0/24 文档
			return false
		case ip4[0] == 198 && ip4[1] == 51 && ip4[2] == 100: // 198.51.100.0/24 文档
			return false
		case ip4[0] == 203 && ip4[1] == 0 && ip4[2] == 113: // 203.0.113.0/24 文档
			return false
		case ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19): // 198.18.0.0/15 基准测试
			return false
		case ip4[0] >= 224: // 组播/保留
			return false
		}
		return true
	}
	// IPv6：必须在全局单播 2000::/3，排除 6to4、Teredo。
	if ip[0]&0xe0 != 0x20 {
		return false
	}
	if ip[0] == 0x20 && ip[1] == 0x02 { // 2002::/16 6to4
		return false
	}
	if ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x00 && ip[3] == 0x00 { // 2001:0000::/32 Teredo
		return false
	}
	return true
}

// downloadImage 下载一张图片，返回 Gemini inlineData（mimeType、base64 data）和字节数。
func downloadImage(ctx context.Context, client *http.Client, rawURL string, budget int64) (map[string]any, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, imageErr("invalid image URL")
	}
	if req.URL.User != nil {
		return nil, 0, imageErr("image URL must not contain credentials")
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Accept", "image/*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, asImageError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, imageErr("image URL did not return a successful response")
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" && !strings.EqualFold(strings.TrimSpace(ce), "identity") {
		return nil, 0, imageErr("image URL returned an unsupported content encoding")
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if v, e := strconv.ParseInt(cl, 10, 64); e == nil && v > budget {
			return nil, 0, imageErr("remote images exceed the size limit for this request")
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, budget+1))
	if err != nil {
		return nil, 0, asImageError(err)
	}
	if int64(len(data)) > budget {
		return nil, 0, imageErr("remote images exceed the size limit for this request")
	}
	mime, ok := sniffImageMime(data, resp.Header.Get("Content-Type"))
	if !ok {
		return nil, 0, imageErr("image URL did not return a supported image")
	}
	return map[string]any{"mimeType": mime, "data": base64.StdEncoding.EncodeToString(data)}, int64(len(data)), nil
}

// asImageError 把下载过程中的错误转成客户端可见错误：已是 RemoteImageError（拨号/重定向校验）时保留，否则给通用说明。
func asImageError(err error) error {
	var rie *RemoteImageError
	if errors.As(err, &rie) {
		return rie
	}
	return imageErr("could not download the image URL; check that it is publicly accessible")
}

// sniffImageMime 按文件头识别图片类型，只放行常见图片格式。
func sniffImageMime(data []byte, contentType string) (string, bool) {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", true
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", true
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif", true
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		if brand := string(data[8:12]); brand == "avif" || brand == "avis" {
			return "image/avif", true
		}
		switch ct := strings.ToLower(strings.TrimSpace(contentType)); ct {
		case "image/heic", "image/heif", "image/avif":
			return ct, true
		}
	}
	return "", false
}
