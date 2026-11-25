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

// verifiedBuilds 的权重贴近真实用户的版本分布：多数在最新，少量落后一档。
var verifiedBuilds = []verifiedBuild{
	{"2.31.2", "1019359392", 65},
	{"2.31.0", "1017595969", 35},
}

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
