package antigravity

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// hubManifestURL 是官方 Antigravity Hub 自动更新清单，首行 version 是官方发布的最新版本。版本与平台无关，
// 取任一平台的清单即可（这里用 mac arm）。只读版本号，不下载二进制。
const hubManifestURL = "https://antigravity-hub-auto-updater-974169037036.us-central1.run.app/manifest/latest-arm64-mac.yml"

// FetchLatestHubVersion 从官方更新清单读出最新客户端版本（形如 2.35.2）。只做版本比对用，不参与请求伪装，
// 所以用普通 UA 直连即可。client 为 nil 时用 http.DefaultClient。
func FetchLatestHubVersion(ctx context.Context, client *http.Client) (string, error) {
	return fetchLatestHubVersionFrom(ctx, client, hubManifestURL)
}

func fetchLatestHubVersionFrom(ctx context.Context, client *http.Client, url string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("官方更新清单返回 %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 64<<10))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		rest, ok := strings.CutPrefix(line, "version:")
		if !ok {
			continue
		}
		v := strings.TrimSpace(rest)
		if !validHubVersion(v) {
			return "", fmt.Errorf("官方更新清单的版本号异常：%q", v)
		}
		return v, nil
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("官方更新清单里没有 version")
}

// validHubVersion 判断是不是形如 1.2.3 的点分数字版本。
func validHubVersion(v string) bool {
	if v == "" {
		return false
	}
	for _, part := range strings.Split(v, ".") {
		if part == "" {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return true
}

// HubVersionNewer 判断 latest 是否比 shipped 新（点分数字逐段比较）。任一不是合法版本号时返回 false，
// 避免因为解析不了就误报落后。
func HubVersionNewer(latest, shipped string) bool {
	if !validHubVersion(latest) || !validHubVersion(shipped) {
		return false
	}
	a := strings.Split(latest, ".")
	b := strings.Split(shipped, ".")
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x, _ = strconv.Atoi(a[i])
		}
		if i < len(b) {
			y, _ = strconv.Atoi(b[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}
