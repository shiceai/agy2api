// Package config 从环境变量读取运行配置（前缀 AGY2API_），不引入配置文件。
//
// 只负责读环境变量和进程级默认值；来源相关的默认值（上游基址、User-Agent、OAuth 端点等）
// 在来源包里，这里留空表示使用来源默认值。
package config

import (
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Config 是进程级运行配置。
type Config struct {
	// Addrs 是 HTTP 监听地址，AGY2API_ADDR 里逗号分隔可写多个（如本机加 WireGuard 内网地址），去重，
	// 至少一个。默认 :8249：避开 8080 这类常用端口和同类工具的端口（8045、8317、7861），
	// 249 是 AGY 在电话键盘上的位置，好记。
	Addrs []string
	// DataDir 存放 SQLite 数据库与自动生成的加密密钥。
	DataDir string
	// AdminPassword 保护管理端；为空则管理端拒绝启动（除非 AllowNoAdminAuth）。
	AdminPassword string
	// AllowNoAdminAuth 仅用于本机开发，跳过管理端鉴权。
	AllowNoAdminAuth bool
	// EncryptionKey 为空时在 DataDir 下生成并复用 agy2api.key。
	EncryptionKey string
	// LogLevel 是日志级别：debug、info、warn、error。
	LogLevel string
	// TrustedProxies 是可信反向代理的地址（IP 或 CIDR）：直连的对端在其中时，才按 X-Forwarded-For、X-Real-IP
	// 取客户端 IP，否则转发头可被客户端伪造。默认回环和内网地址（反代通常和网关在同一台机器或同一个内网）；
	// none 表示都不信。由 server 在启动时解析，写错拒绝启动。
	TrustedProxies []string
	// AdminAllow 限制能访问管理端（接口和页面）的客户端 IP（IP 或 CIDR），空表示不限。
	AdminAllow []string
	// Version 是构建时注入的版本号（见 scripts/build.sh），不来自环境变量，main 在 Load 之后填；空为开发构建。
	Version string

	// 以下覆盖 Antigravity 上游的默认值，留空用 internal/antigravity 里的默认值。

	// UpstreamBases 是 Cloud Code v1internal 的基址，按顺序回退。
	UpstreamBases []string
	// TokenURL 是 OAuth 刷新端点。
	TokenURL string
	// UserInfoURL 用于确认账号身份。
	UserInfoURL string
	// UserAgent 统一指定所有账号的 Cloud Code User-Agent，必须是真实 Antigravity 形态；
	// 留空时每个账号按核验过的官方构建稳定派生（见 antigravity.DeviceUserAgent）。
	UserAgent string

	// PriceSync 定时从厂商官方价格页（Google AI Studio、Anthropic）同步模型价格，用来估算用量的费用。
	// 环境变量默认开启；直接构造 Config 时（测试）为关闭，不访问外网。
	PriceSync bool

	// Update 开启在线升级：每天和打开管理端时查 GitHub 上的新版本，管理端可以一键升级；启动时运行数据目录里在线升级的新程序。
	// 环境变量默认开启；直接构造 Config 时（测试）为关闭，不访问外网。
	Update bool
	// UpdateAPI 换查新版本用的 GitHub API 地址，只用于测试，只能是本机地址；留空用 https://api.github.com。
	UpdateAPI string

	// EmbedMock 在同进程启动模拟上游并把上游地址指向它，仅用于本机开发。
	EmbedMock bool
	// MockAddr 是内置模拟上游的监听地址。
	MockAddr string
}

// Load 读取环境变量构造 Config。
func Load() Config {
	return Config{
		Addrs:            listenAddrs(os.Getenv("AGY2API_ADDR")),
		DataDir:          env("AGY2API_DATA_DIR", "./data"),
		AdminPassword:    os.Getenv("AGY2API_ADMIN_PASSWORD"),
		AllowNoAdminAuth: boolEnv("AGY2API_ALLOW_NO_ADMIN_AUTH"),
		EncryptionKey:    os.Getenv("AGY2API_ENCRYPTION_KEY"),
		LogLevel:         env("AGY2API_LOG_LEVEL", "info"),
		TrustedProxies:   listEnv("AGY2API_TRUSTED_PROXIES", DefaultTrustedProxies),
		AdminAllow:       listEnv("AGY2API_ADMIN_ALLOW", ""),
		UpstreamBases:    splitAndTrim(os.Getenv("AGY2API_UPSTREAM_BASES")),
		TokenURL:         env("AGY2API_TOKEN_URL", ""),
		UserInfoURL:      env("AGY2API_USERINFO_URL", ""),
		UserAgent:        env("AGY2API_USER_AGENT", ""),
		PriceSync:        !offEnv("AGY2API_PRICE_SYNC"),
		Update:           !offEnv("AGY2API_UPDATE"),
		UpdateAPI:        env("AGY2API_UPDATE_API", ""),
		EmbedMock:        boolEnv("AGY2API_EMBED_MOCK"),
		MockAddr:         env("AGY2API_MOCK_ADDR", "127.0.0.1:8099"),
	}
}

// DefaultTrustedProxies 是 AGY2API_TRUSTED_PROXIES 的默认值：回环和内网地址。
const DefaultTrustedProxies = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"

// AdminPasswordWeakness 返回管理口令弱在哪里（少于 12 个字符、只有一类字符），够强时为空串。
// 只用于启动时告警，不拒绝启动：已有部署升级后不能因此起不来。
func AdminPasswordWeakness(pw string) string {
	if utf8.RuneCountInString(pw) < 12 {
		return "少于 12 个字符"
	}
	var lower, upper, digit, other bool
	for _, r := range pw {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	classes := 0
	for _, has := range []bool{lower, upper, digit, other} {
		if has {
			classes++
		}
	}
	if classes < 2 {
		return "只有一类字符"
	}
	return ""
}

// DefaultAddr 是没设置 AGY2API_ADDR（或只有空项）时的监听地址。
const DefaultAddr = ":8249"

// listenAddrs 解析 AGY2API_ADDR：逗号分隔，去掉空项和重复项，一个都没有时用 DefaultAddr。
func listenAddrs(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range strings.Split(raw, ",") {
		if a = strings.TrimSpace(a); a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return []string{DefaultAddr}
	}
	return out
}

// listEnv 读逗号分隔的列表，去掉空项；没设置时用 def。
func listEnv(key, def string) []string {
	var out []string
	for _, p := range strings.Split(env(key, def), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// offEnv 报告默认开启的开关是否被显式关闭。
func offEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

func boolEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func splitAndTrim(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimRight(strings.TrimSpace(p), "/"); p != "" {
			out = append(out, p)
		}
	}
	return out
}
