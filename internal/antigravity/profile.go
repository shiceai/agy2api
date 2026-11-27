package antigravity

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

// verifiedBuild 是官方二进制 --stamp 实读核验过的 (版本, CL) 组合，weight 是分配到它的账号比例。
// 版本和 CL 必须成对来自同一份实读，不能自己拼。
type verifiedBuild struct {
	version, cl string
	weight      int
}

// verifiedBuilds 的权重贴近真实用户的版本分布：多数在官方最新，少量落后一档。表顶是我们对外声明的最高版本，
// 官方 Hub 更新后用它和官方最新比，落后了在管理端提示（见 hubversion.go）。
// CL 来自参考项目对官方 language_server 的 --stamp 实读：2.34.0、2.32.2 与 gcl2api-plus、CLIProxyAPI 两家吻合。
var verifiedBuilds = []verifiedBuild{
	{"2.35.2", "1029311855", 60}, // 官方最新（CLIProxyAPI --stamp 实读）
	{"2.34.0", "1026637980", 28}, // gcl2api-plus 与 CLIProxyAPI --stamp 双向吻合
	{"2.33.1", "1026001617", 12}, // gcl2api-plus --stamp 实读
}

// LatestShippedHubVersion 返回我们对外声明的最高 Antigravity 客户端版本（verifiedBuilds 表顶）。
func LatestShippedHubVersion() string { return verifiedBuilds[0].version }

// 只用实抓过的平台；macOS、Windows 的 UA 写法没有实抓依据，不引入。
const deviceOS, deviceArch = "linux", "amd64"

// deviceProfileSalt 只用于派生，改了会让所有账号的画像一起变。
const deviceProfileSalt = "agy2api:antigravity:device-profile:v1\x00"

// DeviceUserAgent 按账号稳定派生设备画像对应的 User-Agent：同一账号永远一致（真实客户端的安装是
// 稳定的，不能随机），不同账号按权重分散到核验过的构建。accountKey 用账号邮箱，不区分大小写。
func DeviceUserAgent(accountKey string) string {
	sum := sha256.Sum256([]byte(deviceProfileSalt + strings.ToLower(strings.TrimSpace(accountKey))))
	bucket := int(binary.BigEndian.Uint64(sum[:8]) % 100)
	total := 0
	for _, b := range verifiedBuilds {
		total += b.weight
	}
	chosen := verifiedBuilds[0]
	cursor := 0
	for _, b := range verifiedBuilds {
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
