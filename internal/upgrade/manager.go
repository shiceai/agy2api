package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Repo 是发布新版本的 GitHub 仓库。
const Repo = "shiceai/agy2api"

const defaultAPI = "https://api.github.com"

// 进度的阶段。
const (
	StateDownloading = "downloading"
	StateVerifying   = "verifying"
	StateBackingUp   = "backing_up"
	StateRestarting  = "restarting"
	StateFailed      = "failed"
)

// Options 配置 Manager。
type Options struct {
	DataDir string
	// Version 是运行中的版本。
	Version string
	// Enabled 为假（AGY2API_UPDATE=off）时不连 GitHub，也不能升级。
	Enabled bool
	// API 是 GitHub API 的地址，空为 https://api.github.com；只用于测试，只能指向本机。
	API string
	// Backup 在换上新程序之前把数据库备份到 dst。
	Backup func(ctx context.Context, dst string) error
}

// Release 是 GitHub 上最新的正式版本。
type Release struct {
	Version     string `json:"version"`
	Notes       string `json:"notes"`
	URL         string `json:"url,omitempty"`
	PublishedAt int64  `json:"published_at,omitempty"`
	assets      map[string]asset
}

type asset struct {
	url  string
	size int64
}

// Progress 是升级进度。State 为空表示没有在升级。
type Progress struct {
	State    string `json:"state,omitempty"`
	Target   string `json:"target,omitempty"`
	Received int64  `json:"received,omitempty"`
	Total    int64  `json:"total,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Failed 是停用的新程序。
type Failed struct {
	Version string `json:"version"`
	Reason  string `json:"reason"`
}

// View 是管理端看到的版本信息。
type View struct {
	// Version 是运行中的版本；运行的是在线升级的程序时，BaseVersion 是自带程序（镜像、安装包里的）的版本，
	// InstalledAt 是在线升级的时间。
	Version     string `json:"version"`
	BaseVersion string `json:"base_version,omitempty"`
	InstalledAt int64  `json:"installed_at,omitempty"`
	// Deployment 是部署方式：docker、systemd、binary（直接运行）。
	Deployment string `json:"deployment"`
	// CanCheck 表示能查新版本（Windows 能查不能升级）；CanUpgrade 表示能在线升级，不能时 UnsupportedReason 是原因。
	CanCheck          bool   `json:"can_check"`
	CanUpgrade        bool   `json:"can_upgrade"`
	UnsupportedReason string `json:"unsupported_reason,omitempty"`
	// Latest 是最近一次查到的最新正式版本，CheckError 是最近一次检查的错误。
	Latest          *Release `json:"latest,omitempty"`
	UpdateAvailable bool     `json:"update_available"`
	CheckedAt       int64    `json:"checked_at,omitempty"`
	CheckError      string   `json:"check_error,omitempty"`
	Failed          *Failed  `json:"failed,omitempty"`
	Progress        Progress `json:"progress"`
}

// Manager 查新版本、执行在线升级和撤销。
type Manager struct {
	opts       Options
	api        *url.URL
	local      bool // API 指向本机（测试）：下载地址也允许同一个本机地址
	client     *http.Client
	deployment string
	base       string // 运行的是在线升级的程序时，自带程序的版本
	overlay    bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	checkMu sync.Mutex // 同一时刻只查一次

	mu        sync.Mutex
	latest    *Release
	checkedAt int64
	checkErr  string
	notified  string // 已在日志里提示过的新版本
	progress  Progress

	restart     chan struct{}
	restartOnce sync.Once

	// runVersion 运行下载的程序取它的版本号，测试时替换。
	runVersion func(ctx context.Context, path string) (string, error)
}

// New 构造 Manager。API 不是本机地址时报错。
func New(opts Options) (*Manager, error) {
	raw := opts.API
	if raw == "" {
		raw = defaultAPI
	}
	api, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || (api.Scheme != "https" && api.Scheme != "http") || api.Host == "" {
		return nil, fmt.Errorf("AGY2API_UPDATE_API 不是有效的地址：%q", opts.API)
	}
	local := opts.API != ""
	if local && !isLoopback(api.Hostname()) {
		return nil, fmt.Errorf("AGY2API_UPDATE_API 只用于测试，只能是本机地址：%q", opts.API)
	}
	m := &Manager{
		opts:       opts,
		api:        api,
		local:      local,
		deployment: detectDeployment(),
		restart:    make(chan struct{}),
		runVersion: runVersion,
	}
	m.base, m.overlay = Running()
	transport := http.DefaultTransport.(*http.Transport).Clone() // 带 HTTPS_PROXY 等环境变量的代理设置
	m.client = &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("跳转次数太多")
			}
			if !m.allowed(req.URL) {
				return fmt.Errorf("跳转到了不认识的地址 %s", req.URL.Host)
			}
			return nil
		},
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	return m, nil
}

// Start 启动后台检查：启动一分钟后查一次，之后每天一次。
func (m *Manager) Start() {
	if !m.canCheck() {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTimer(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-t.C:
			}
			ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
			if err := m.Check(ctx, time.Hour); err != nil {
				slog.Warn("检查新版本失败", "err", err)
			}
			cancel()
			t.Reset(24 * time.Hour)
		}
	}()
}

// Close 停止后台检查和进行中的升级。
func (m *Manager) Close() {
	m.cancel()
	m.wg.Wait()
}

// RestartRequested 在升级或撤销完成、需要重启时关闭。
func (m *Manager) RestartRequested() <-chan struct{} { return m.restart }

func (m *Manager) requestRestart() { m.restartOnce.Do(func() { close(m.restart) }) }

// canCheck 报告能不能查新版本：开发构建没法比较版本。
func (m *Manager) canCheck() bool { return m.opts.Enabled && IsRelease(m.opts.Version) }

// unsupportedReason 返回不能在线升级的原因，能升级时为空。
func (m *Manager) unsupportedReason() string {
	switch {
	case !m.opts.Enabled:
		return "已关闭在线升级（AGY2API_UPDATE=off）"
	case !IsRelease(m.opts.Version):
		return "开发构建不支持在线升级"
	case !execSupported:
		return "Windows 不支持在线升级，请从 Releases 下载新版本替换"
	case (runtime.GOOS != "linux" && runtime.GOOS != "darwin") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64"):
		return fmt.Sprintf("没有 %s/%s 的发布包", runtime.GOOS, runtime.GOARCH)
	}
	return ""
}

// Check 查最新的正式版本。距上次检查不到 maxAge 时不重复查。
func (m *Manager) Check(ctx context.Context, maxAge time.Duration) error {
	if !m.canCheck() {
		return errors.New(m.unsupportedReason())
	}
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	m.mu.Lock()
	fresh := m.checkedAt > 0 && time.Since(time.UnixMilli(m.checkedAt)) < maxAge
	m.mu.Unlock()
	if fresh {
		return nil
	}
	rel, err := m.fetchLatest(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkedAt = time.Now().UnixMilli()
	if err != nil {
		m.checkErr = err.Error()
		return err
	}
	m.latest, m.checkErr = rel, ""
	if newer(rel.Version, m.opts.Version) && m.notified != rel.Version {
		m.notified = rel.Version
		slog.Info("有新版本", "version", rel.Version, "current", m.opts.Version)
	}
	return nil
}

// UpdateVersion 返回查到的更新的版本，没有时为空。
func (m *Manager) UpdateVersion() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.latest != nil && newer(m.latest.Version, m.opts.Version) {
		return m.latest.Version
	}
	return ""
}

// View 返回版本信息和升级进度。
func (m *Manager) View() View {
	v := View{
		Version:           m.opts.Version,
		BaseVersion:       m.base,
		Deployment:        m.deployment,
		UnsupportedReason: m.unsupportedReason(),
	}
	v.CanCheck, v.CanUpgrade = m.canCheck(), v.UnsupportedReason == ""
	if st, err := readState(upgradeDir(m.opts.DataDir)); err == nil && st != nil {
		if m.overlay && st.Version == m.opts.Version {
			v.InstalledAt = st.InstalledAt
		}
		if st.Failed {
			v.Failed = &Failed{Version: st.Version, Reason: st.Reason}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v.Latest, v.CheckedAt, v.CheckError, v.Progress = m.latest, m.checkedAt, m.checkErr, m.progress
	v.UpdateAvailable = m.latest != nil && newer(m.latest.Version, m.opts.Version)
	return v
}

func (m *Manager) busyLocked() bool {
	switch m.progress.State {
	case StateDownloading, StateVerifying, StateBackingUp, StateRestarting:
		return true
	}
	return false
}

// Upgrade 开始升级到 target（必须是最近一次查到的最新版本），在后台下载、校验、备份数据库、换上新程序，
// 完成后请求重启。不能开始时返回原因。
func (m *Manager) Upgrade(target string) error {
	if r := m.unsupportedReason(); r != "" {
		return errors.New(r)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.busyLocked():
		return errors.New("正在升级，请等它完成")
	case m.latest == nil || m.latest.Version != target:
		return errors.New("要升级的版本和查到的最新版本不一致，请重新检查")
	case !newer(target, m.opts.Version):
		return fmt.Errorf("当前已是 %s 或更新的版本", target)
	}
	rel := m.latest
	m.progress = Progress{State: StateDownloading, Target: target}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		if err := m.install(m.ctx, rel); err != nil {
			slog.Error("在线升级失败", "target", target, "err", err)
			m.setProgress(func(p *Progress) { p.State, p.Error = StateFailed, err.Error() })
			return
		}
		slog.Info("在线升级的程序已就绪，重启", "target", target)
		m.setProgress(func(p *Progress) { p.State = StateRestarting })
		m.requestRestart()
	}()
	return nil
}

// Revert 撤销在线升级：删掉数据目录里的新程序。运行的正是它时请求重启、回到自带的程序，restart 为真。
func (m *Manager) Revert() (restart bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busyLocked() {
		return false, errors.New("正在升级，请等它完成")
	}
	dir := upgradeDir(m.opts.DataDir)
	st, err := readState(dir)
	if err != nil {
		return false, err
	}
	if st == nil && !m.overlay {
		return false, errors.New("没有在线升级的程序")
	}
	if err := removeOverlay(dir); err != nil {
		return false, fmt.Errorf("删除在线升级的程序失败：%w", err)
	}
	if !m.overlay {
		return false, nil
	}
	slog.Info("撤销在线升级，重启回到自带的程序", "version", m.base)
	m.progress = Progress{State: StateRestarting, Target: m.base}
	m.requestRestart()
	return true, nil
}

func (m *Manager) setProgress(f func(*Progress)) {
	m.mu.Lock()
	f(&m.progress)
	m.mu.Unlock()
}

// ghRelease 是 GitHub API 返回的 Release 里用到的字段。
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// fetchLatest 查 GitHub 上最新的正式版本（releases/latest 不含预发布）。
func (m *Manager) fetchLatest(ctx context.Context) (*Release, error) {
	resp, err := m.get(ctx, m.api.String()+"/repos/"+Repo+"/releases/latest")
	if se := statusError(0); errors.As(err, &se) && se == http.StatusNotFound {
		return nil, errors.New("GitHub 上还没有正式版本")
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var gr ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&gr); err != nil {
		return nil, fmt.Errorf("GitHub 返回的内容读不了：%w", err)
	}
	if !IsRelease(gr.TagName) {
		return nil, fmt.Errorf("最新版本的标签 %q 不是正式的版本号", gr.TagName)
	}
	rel := &Release{Version: gr.TagName, Notes: gr.Body, assets: map[string]asset{}}
	if u, err := url.Parse(gr.HTMLURL); err == nil && m.allowed(u) {
		rel.URL = gr.HTMLURL
	}
	if !gr.PublishedAt.IsZero() {
		rel.PublishedAt = gr.PublishedAt.UnixMilli()
	}
	for _, a := range gr.Assets {
		rel.assets[a.Name] = asset{url: a.URL, size: a.Size}
	}
	return rel, nil
}

// get 发 GET 请求，地址和跳转都只认 GitHub（测试时是 API 所在的本机地址）。返回的响应状态是 200。
func (m *Manager) get(ctx context.Context, raw string) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err != nil || !m.allowed(u) {
		return nil, fmt.Errorf("不认识的下载地址 %q", raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "agy2api/"+m.opts.Version)
	if strings.HasPrefix(raw, m.api.String()+"/") {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 GitHub：%w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	resp.Body.Close()
	return nil, statusError(resp.StatusCode)
}

// statusError 是 GitHub 返回的非 200 状态码。
type statusError int

func (e statusError) Error() string {
	switch int(e) {
	case http.StatusForbidden, http.StatusTooManyRequests:
		return "GitHub 暂时限制了访问，稍后再试"
	default:
		return fmt.Sprintf("GitHub 返回 %d", int(e))
	}
}

// allowed 报告能不能访问这个地址：GitHub 的 API 和下载域名；API 指向本机（测试）时还有那个本机地址。
func (m *Manager) allowed(u *url.URL) bool {
	if m.local {
		return u.Scheme == m.api.Scheme && u.Host == m.api.Host
	}
	if u.Scheme != "https" || u.Port() != "" {
		return false
	}
	switch u.Hostname() {
	case "api.github.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com":
		return true
	}
	return false
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// detectDeployment 判断部署方式，只用来在管理端给出对应的提示。
func detectDeployment() string {
	// 先看 systemd：由 systemd 拉起的服务（包括跑在容器里的 systemd）都按 systemd 部署。
	if os.Getenv("INVOCATION_ID") != "" {
		return "systemd"
	}
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return "docker"
		}
	}
	return "binary"
}

// backupPath 是升级到 version 之前的数据库备份。
func backupPath(dataDir, version string) string {
	return filepath.Join(dataDir, backupDir, "agy2api-before-"+version+".db")
}
