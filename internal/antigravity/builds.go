package antigravity

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

// UA 对外声明的官方客户端构建表：(版本, CL) 成对，版本随官方发布前进，CL 与版本成对、只来自官方
// language_server 构建戳的实读，不省略 cl=、不把新版本配旧 CL（见 ua-builds.json 与
// .github/workflows/ua-builds.yml）。表由内置 baseline 起步，运行时可被远程刷新整表替换（见 SetActiveBuilds），
// 这样版本/CL 与发版脱钩：维护流水线更新 ua-builds.json 后，运行中的实例定时拉取即跟进，无需发版。

// Build 是一条 (版本, CL) 对，用于远程刷新输入和解析 ua-builds.json。
type Build struct {
	Version string `json:"version"`
	CL      string `json:"cl"`
}

// uaBuildsFile 是 ua-builds.json 的结构：builds 按新到旧排列的已核验构建。
type uaBuildsFile struct {
	Builds []Build `json:"builds"`
}

//go:embed ua-builds.json
var builtinUABuildsRaw []byte

// DefaultUABuildsURL 是 ua-builds.json 的默认地址（仓库 main 分支的 raw），后台定时刷新用。
const DefaultUABuildsURL = "https://raw.githubusercontent.com/shiceai/agy2api/main/internal/antigravity/ua-builds.json"

// buildWeights 是加权选号用的版本分布（新到旧）：多数账号声明最新、少量落后一档，贴近真实用户的升级曲线。
// 活动表取最新的 len(buildWeights) 条参与派生；再旧的只留作记录、不对外声明（声明太旧的版本反而可疑）。
var buildWeights = []int{60, 28, 12}

// activeBuilds 持有当前生效的加权构建池，热路径（DeviceUserAgent）无锁读。启动置为内置 baseline。
var activeBuilds atomic.Pointer[[]verifiedBuild]

func init() {
	weighted, err := weightedFromJSON(builtinUABuildsRaw)
	if err != nil {
		panic("内置 ua-builds.json 无效: " + err.Error())
	}
	activeBuilds.Store(&weighted)
}

// currentBuilds 返回当前生效的加权构建池（至少一条）。
func currentBuilds() []verifiedBuild {
	return *activeBuilds.Load()
}

// SetActiveBuilds 用一组 (版本, CL) 对整表替换活动表：校验通过才替换，失败返回错误、保留当前表。
func SetActiveBuilds(builds []Build) error {
	if err := validateBuilds(builds); err != nil {
		return err
	}
	weighted := weightedBuilds(builds)
	activeBuilds.Store(&weighted)
	return nil
}

// FetchRemoteBuilds 从 url 拉取并解析 ua-builds.json，返回按新到旧的 (版本, CL) 对。只读构建元数据，
// 不参与请求伪装，用普通 UA 直连即可；client 为 nil 时用 http.DefaultClient。
func FetchRemoteBuilds(ctx context.Context, client *http.Client, url string) ([]Build, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("ua-builds.json 返回 %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	return parseUABuilds(raw)
}

// parseUABuilds 解析并校验 ua-builds.json 的内容。
func parseUABuilds(raw []byte) ([]Build, error) {
	var f uaBuildsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析 ua-builds.json: %w", err)
	}
	if err := validateBuilds(f.Builds); err != nil {
		return nil, err
	}
	return f.Builds, nil
}

// validateBuilds 校验一组构建：非空、版本是点分数字、CL 是非空且不超长的纯数字、版本不重复。
func validateBuilds(builds []Build) error {
	if len(builds) == 0 {
		return fmt.Errorf("ua-builds 没有构建")
	}
	seen := make(map[string]bool, len(builds))
	for _, b := range builds {
		if !validHubVersion(b.Version) {
			return fmt.Errorf("ua-builds 版本号异常: %q", b.Version)
		}
		if !validChangelist(b.CL) {
			return fmt.Errorf("ua-builds CL 异常: %q", b.CL)
		}
		if seen[b.Version] {
			return fmt.Errorf("ua-builds 版本重复: %q", b.Version)
		}
		seen[b.Version] = true
	}
	return nil
}

// validChangelist 判断是不是非空、不超 20 位的纯数字 CL。
func validChangelist(cl string) bool {
	if cl == "" || len(cl) > 20 {
		return false
	}
	for _, c := range cl {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// weightedBuilds 取最新的 len(buildWeights) 条，按 buildWeights 赋权，构成加权池。调用前须先 validateBuilds。
func weightedBuilds(builds []Build) []verifiedBuild {
	n := len(builds)
	if n > len(buildWeights) {
		n = len(buildWeights)
	}
	out := make([]verifiedBuild, n)
	for i := 0; i < n; i++ {
		out[i] = verifiedBuild{version: builds[i].Version, cl: builds[i].CL, weight: buildWeights[i]}
	}
	return out
}

func weightedFromJSON(raw []byte) ([]verifiedBuild, error) {
	builds, err := parseUABuilds(raw)
	if err != nil {
		return nil, err
	}
	return weightedBuilds(builds), nil
}
