package antigravity

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

// verifiedBuild 是一条对外声明的官方客户端构建：(版本, CL) 成对核验，weight 是分配到它的账号比例。
// 版本和 CL 必须成对来自官方 language_server 构建戳的实读，不能自己拼；表的来源与刷新见 builds.go、ua-builds.json。
type verifiedBuild struct {
	version, cl string
	weight      int
}

// LatestShippedHubVersion 返回我们当前对外声明的最高 Antigravity 客户端版本（活动表表顶）。
// 官方 Hub 更新后用它和官方最新比，落后了在管理端提示（见 hubversion.go）。
func LatestShippedHubVersion() string { return currentBuilds()[0].version }

// 只用实抓过的平台；macOS、Windows 的 UA 写法没有实抓依据，不引入。
const deviceOS, deviceArch = "linux", "amd64"

// deviceProfileSalt 只用于派生，改了会让所有账号的画像一起变。
const deviceProfileSalt = "agy2api:antigravity:device-profile:v1\x00"

// DeviceUserAgent 按账号稳定派生设备画像对应的 User-Agent：同一账号永远一致（真实客户端的安装是
// 稳定的，不能随机），不同账号按权重分散到核验过的构建。accountKey 用账号邮箱，不区分大小写。
func DeviceUserAgent(accountKey string) string {
	builds := currentBuilds()
	sum := sha256.Sum256([]byte(deviceProfileSalt + strings.ToLower(strings.TrimSpace(accountKey))))
	bucket := int(binary.BigEndian.Uint64(sum[:8]) % 100)
	total := 0
	for _, b := range builds {
		total += b.weight
	}
	chosen := builds[0]
	cursor := 0
	for _, b := range builds {
		cursor += b.weight
		if bucket < cursor*100/total {
			chosen = b
			break
		}
	}
	return userAgentFor(chosen)
}

func userAgentFor(b verifiedBuild) string {
	return fmt.Sprintf("antigravity/hub/%s (aidev_client; os_type=%s; arch=%s; cl=%s)", b.version, deviceOS, deviceArch, b.cl)
}
