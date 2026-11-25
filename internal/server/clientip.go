package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
)

// 客户端 IP：前面有反向代理时，直连的对端是反代，真实客户端在转发头里。转发头客户端自己也能写，所以只有对端是
// 可信反代（AGY2API_TRUSTED_PROXIES）时才看：X-Forwarded-For 从右往左跳过可信反代，第一个不可信的地址就是客户端
// （反代把它看到的对端追加在最右边，客户端伪造的值只会在更左边）；没有 X-Forwarded-For 时看 X-Real-IP。
// 解析出的 IP 用于访问日志、用量记录和管理端白名单（AGY2API_ADMIN_ALLOW）。

// parsePrefixes 解析 IP 或 CIDR 列表（单个 IP 当成 /32 或 /128），none 表示空列表。
func parsePrefixes(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		if strings.EqualFold(s, "none") {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("invalid IP or CIDR %q", s)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func containsAddr(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// parseHostAddr 解析 "IP"、"IP:端口"、"[IPv6]:端口"，IPv4 映射的 IPv6 地址还原成 IPv4。
func parseHostAddr(s string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), true
	}
	return netip.Addr{}, false
}

// resolveClientIP 按上面的规则取请求的客户端 IP。RemoteAddr 解析不了时 ok 为 false。
func resolveClientIP(r *http.Request, trusted []netip.Prefix) (netip.Addr, bool) {
	peer, ok := parseHostAddr(r.RemoteAddr)
	if !ok || !containsAddr(trusted, peer) {
		return peer, ok
	}
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		hops := strings.Split(strings.Join(xff, ","), ",")
		client := peer
		for i := len(hops) - 1; i >= 0; i-- {
			a, ok := parseHostAddr(strings.TrimSpace(hops[i]))
			if !ok {
				break // 写坏的一跳：再往左的都不可靠，停在最后一个可信反代
			}
			client = a
			if !containsAddr(trusted, a) {
				break
			}
		}
		return client, true
	}
	if a, ok := parseHostAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ok {
		return a, true
	}
	return peer, true
}

// realIP 把 r.RemoteAddr 改成客户端 IP（不带端口），替代无条件信任转发头的 chi RealIP。
func realIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip, ok := resolveClientIP(r, trusted); ok {
				r.RemoteAddr = ip.String()
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isGatewayPath 报告路径是不是网关接口（Gemini 入站），它们不受管理端白名单限制。
func isGatewayPath(p string) bool {
	return strings.HasPrefix(p, "/v1beta/") || strings.HasPrefix(p, "/v1/")
}

// adminAllowlist 按 AGY2API_ADMIN_ALLOW 限制管理端的接口和页面（网关接口不受限），放在口令校验之前：
// 白名单外的地址连口令都试不了。没设置白名单时不限。
func (s *Service) adminAllowlist(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.adminAllow) == 0 || isGatewayPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if a, ok := parseHostAddr(r.RemoteAddr); ok && containsAddr(s.adminAllow, a) {
			next.ServeHTTP(w, r)
			return
		}
		slog.InfoContext(r.Context(), "管理端访问被拒：地址不在 AGY2API_ADMIN_ALLOW 里", "remote", r.RemoteAddr, "path", r.URL.Path)
		const msg = "admin access is not allowed from this address"
		if strings.HasPrefix(r.URL.Path, "/api/") {
			adminError(w, http.StatusForbidden, msg)
			return
		}
		http.Error(w, msg, http.StatusForbidden)
	})
}
