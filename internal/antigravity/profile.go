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

// profileVersion 取 User-Agent 里声明的客户端版本（antigravity/hub/<版本> (...)），不是这种形态时返回 false。
func profileVersion(ua string) (string, bool) {
	rest, ok := strings.CutPrefix(ua, "antigravity/hub/")
	if !ok {
		return "", false
	}
	v, _, ok := strings.Cut(rest, " ")
	if !ok || !validHubVersion(v) {
		return "", false
	}
	return v, true
}

// profileRetired 判断账号保存的画像是否已经淘汰：声明的版本比当前参与派生的构建（活动表最新几条）里最旧的还旧。
// 画像平时保持不变（真实客户端的安装是稳定的），但真实客户端会随官方发布自动更新，一直声明淘汰的旧版本反而显眼，
// 淘汰后改按邮箱重新派生（落在同一档权重上的当前构建）。比活动表还新的不算（构建表刷新失败、退回内置表时），只升不降；
// 不是我们派生的形态时不动。
func profileRetired(ua string) bool {
	v, ok := profileVersion(ua)
	if !ok {
		return false
	}
	builds := currentBuilds()
	return HubVersionNewer(builds[len(builds)-1].version, v)
}

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
