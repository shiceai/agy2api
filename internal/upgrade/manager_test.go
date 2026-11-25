package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeGitHub 模拟 GitHub：最新版本 v0.2.0，附带当前平台的发布包和 SHA256SUMS.txt。
type fakeGitHub struct {
	srv     *httptest.Server
	pkg     []byte // 发布包
	sums    string // SHA256SUMS.txt
	latest  int    // releases/latest 的状态码，0 为 200
	version string
}

const fakeBinary = "#!/bin/sh\necho fake agy2api\n"

func pkgName(version string) string {
	return fmt.Sprintf("agy2api_%s_%s_%s", strings.TrimPrefix(version, "v"), runtime.GOOS, runtime.GOARCH)
}

// makePackage 按发布流程的格式打包：<包名>/agy2api 加上说明文件。
func makePackage(t *testing.T, version string, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		hdr := &tar.Header{Name: pkgName(version) + "/" + name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if strings.HasPrefix(body, "->") { // 符号链接
			hdr = &tar.Header{Name: pkgName(version) + "/" + name, Linkname: body[2:], Typeflag: tar.TypeSymlink}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{version: "v0.2.0"}
	f.pkg = makePackage(t, f.version, map[string]string{"agy2api": fakeBinary, "README.md": "readme"})
	sum := sha256.Sum256(f.pkg)
	f.sums = hex.EncodeToString(sum[:]) + " *" + pkgName(f.version) + ".tar.gz\n" + strings.Repeat("0", 64) + "  install.sh\n"
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			if f.latest != 0 {
				w.WriteHeader(f.latest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":     f.version,
				"body":         "### 功能\n\n- **在线升级。** 一键升级。",
				"html_url":     base + "/releases/tag/" + f.version,
				"published_at": "2026-11-26T08:00:00Z",
				"assets": []map[string]any{
					{"name": pkgName(f.version) + ".tar.gz", "browser_download_url": base + "/dl/pkg", "size": len(f.pkg)},
					{"name": "SHA256SUMS.txt", "browser_download_url": base + "/dl/sums", "size": len(f.sums)},
				},
			})
		case "/dl/pkg":
			_, _ = w.Write(f.pkg)
		case "/dl/sums":
			// 和真实 GitHub 一样，下载地址先跳转一次。
			http.Redirect(w, r, "/dl/sums-file", http.StatusFound)
		case "/dl/sums-file":
			_, _ = w.Write([]byte(f.sums))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newTestManager(t *testing.T, f *fakeGitHub, dataDir string) *Manager {
	t.Helper()
	m, err := New(Options{
		DataDir: dataDir,
		Version: "v0.1.0",
		Enabled: true,
		API:     f.srv.URL,
		Backup: func(_ context.Context, dst string) error {
			return os.WriteFile(dst, []byte("db"), 0o600)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.runVersion = func(context.Context, string) (string, error) { return f.version, nil }
	t.Cleanup(m.Close)
	return m
}

func TestCheckFindsNewerRelease(t *testing.T) {
	f := newFakeGitHub(t)
	m := newTestManager(t, f, t.TempDir())
	if err := m.Check(context.Background(), 0); err != nil {
		t.Fatalf("Check: %v", err)
	}
	v := m.View()
	if !v.UpdateAvailable || v.Latest == nil || v.Latest.Version != "v0.2.0" || v.CheckedAt == 0 || v.CheckError != "" {
		t.Fatalf("View = %+v", v)
	}
	if v.Latest.PublishedAt != time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC).UnixMilli() || !strings.Contains(v.Latest.Notes, "在线升级") {
		t.Fatalf("Latest = %+v", v.Latest)
	}
	if got := m.UpdateVersion(); got != "v0.2.0" {
		t.Fatalf("UpdateVersion = %q", got)
	}

	// 没有正式版本时报可读的原因，保留上次查到的结果。
	f.latest = http.StatusNotFound
	if err := m.Check(context.Background(), 0); err == nil || err.Error() != "GitHub 上还没有正式版本" {
		t.Fatalf("404 时 Check = %v", err)
	}
	if v := m.View(); v.CheckError == "" || v.Latest == nil {
		t.Fatalf("失败后 View = %+v", v)
	}
	f.latest = http.StatusForbidden
	if err := m.Check(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "限制") {
		t.Fatalf("403 时 Check = %v", err)
	}
}

func TestCheckSameVersionIsNotUpdate(t *testing.T) {
	f := newFakeGitHub(t)
	f.version = "v0.1.0"
	m := newTestManager(t, f, t.TempDir())
	if err := m.Check(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if v := m.View(); v.UpdateAvailable || m.UpdateVersion() != "" {
		t.Fatalf("同版本不算更新：%+v", v)
	}
}

func TestCheckDisabledOrDevBuild(t *testing.T) {
	for _, opts := range []Options{{Version: "v0.1.0", Enabled: false}, {Version: "v0.1.0-3-gabc1234", Enabled: true}} {
		opts.DataDir = t.TempDir()
		m, err := New(opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Check(context.Background(), 0); err == nil {
			t.Errorf("%+v：不该去查新版本", opts)
		}
		if v := m.View(); v.CanUpgrade || v.UnsupportedReason == "" {
			t.Errorf("%+v：View = %+v", opts, v)
		}
		if err := m.Upgrade("v0.2.0"); err == nil {
			t.Errorf("%+v：不该能升级", opts)
		}
		m.Close()
	}
}

// 下载、校验、备份、放进数据目录：新程序和记录就位，旧备份被替换。
func TestInstall(t *testing.T) {
	f := newFakeGitHub(t)
	dataDir := t.TempDir()
	m := newTestManager(t, f, dataDir)
	if err := m.Check(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	old := backupPath(dataDir, "v0.1.5")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.install(context.Background(), m.latest); err != nil {
		t.Fatalf("install: %v", err)
	}
	dir := upgradeDir(dataDir)
	got, err := os.ReadFile(filepath.Join(dir, binName))
	if err != nil || string(got) != fakeBinary {
		t.Fatalf("新程序 = %q, %v", got, err)
	}
	st, err := readState(dir)
	if err != nil || st == nil {
		t.Fatalf("readState: %v, %v", st, err)
	}
	sum := sha256.Sum256([]byte(fakeBinary))
	if st.Version != "v0.2.0" || st.SHA256 != hex.EncodeToString(sum[:]) || st.OS != runtime.GOOS || st.Arch != runtime.GOARCH ||
		st.From != "v0.1.0" || st.InstalledAt == 0 || st.Attempts != 0 || st.Confirmed {
		t.Fatalf("记录 = %+v", st)
	}
	if _, err := os.Stat(backupPath(dataDir, "v0.2.0")); err != nil {
		t.Fatalf("应有升级前的备份：%v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("更早的备份应被删掉：%v", err)
	}
	for _, leftover := range []string{"download.tar.gz", binName + ".new"} {
		if _, err := os.Stat(filepath.Join(dir, leftover)); !os.IsNotExist(err) {
			t.Fatalf("临时文件 %s 没清理：%v", leftover, err)
		}
	}
	if m.progress.Received != int64(len(f.pkg)) || m.progress.Total != int64(len(f.pkg)) {
		t.Fatalf("进度 = %+v", m.progress)
	}
}

// 校验不过、版本不符、包里没有程序、备份失败：都不放进数据目录。
func TestInstallRejects(t *testing.T) {
	cases := map[string]func(f *fakeGitHub, m *Manager){
		"校验和不符": func(f *fakeGitHub, _ *Manager) {
			f.sums = strings.Repeat("a", 64) + "  " + pkgName(f.version) + ".tar.gz\n"
		},
		"SHA256SUMS 里没有": func(f *fakeGitHub, _ *Manager) { f.sums = "" },
		"程序报告的版本不对": func(_ *fakeGitHub, m *Manager) {
			m.runVersion = func(context.Context, string) (string, error) { return "v0.1.9", nil }
		},
		"包里的程序是符号链接": func(f *fakeGitHub, _ *Manager) {
			f.pkg = makePackage(t, f.version, map[string]string{"agy2api": "->/bin/sh"})
		},
		"包里没有程序": func(f *fakeGitHub, _ *Manager) {
			f.pkg = makePackage(t, f.version, map[string]string{"README.md": "x", "../agy2api": fakeBinary})
		},
		"备份失败": func(_ *fakeGitHub, m *Manager) {
			m.opts.Backup = func(context.Context, string) error { return fmt.Errorf("disk full") }
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeGitHub(t)
			dataDir := t.TempDir()
			m := newTestManager(t, f, dataDir)
			if err := m.Check(context.Background(), 0); err != nil {
				t.Fatal(err)
			}
			mutate(f, m)
			if strings.HasPrefix(name, "包里") { // 包换了，校验和跟着换，测的是解包
				sum := sha256.Sum256(f.pkg)
				f.sums = hex.EncodeToString(sum[:]) + "  " + pkgName(f.version) + ".tar.gz\n"
				m.latest.assets[pkgName(f.version)+".tar.gz"] = asset{url: f.srv.URL + "/dl/pkg", size: int64(len(f.pkg))}
			}
			err := m.install(context.Background(), m.latest)
			if err == nil {
				t.Fatal("应该失败")
			}
			t.Log(err)
			if _, err := os.Stat(filepath.Join(upgradeDir(dataDir), binName)); !os.IsNotExist(err) {
				t.Fatalf("失败时不应放进新程序：%v", err)
			}
			if st, _ := readState(upgradeDir(dataDir)); st != nil {
				t.Fatalf("失败时不应写记录：%+v", st)
			}
		})
	}
}

// Upgrade 在后台装好后请求重启；只能升级到查到的最新版本，同时只能有一个。
func TestUpgradeRequestsRestart(t *testing.T) {
	if !execSupported {
		t.Skip("这个平台不支持在线升级")
	}
	f := newFakeGitHub(t)
	m := newTestManager(t, f, t.TempDir())
	if err := m.Upgrade("v0.2.0"); err == nil {
		t.Fatal("没查过新版本时不能升级")
	}
	if err := m.Check(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if err := m.Upgrade("v0.3.0"); err == nil {
		t.Fatal("只能升级到查到的版本")
	}
	if err := m.Upgrade("v0.2.0"); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	select {
	case <-m.RestartRequested():
	case <-time.After(10 * time.Second):
		t.Fatalf("没有请求重启，进度 %+v", m.View().Progress)
	}
	if p := m.View().Progress; p.State != StateRestarting || p.Target != "v0.2.0" {
		t.Fatalf("进度 = %+v", p)
	}
	if err := m.Upgrade("v0.2.0"); err == nil {
		t.Fatal("重启中不能再升级")
	}
}

// 撤销：没在运行新程序时只删文件，不重启。
func TestRevertNotRunning(t *testing.T) {
	f := newFakeGitHub(t)
	dataDir := t.TempDir()
	m := newTestManager(t, f, dataDir)
	if _, err := m.Revert(); err == nil {
		t.Fatal("没有在线升级的程序时应报错")
	}
	dir := upgradeDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeState(dir, &State{Version: "v0.2.0", Failed: true, Reason: "连续 3 次没能启动"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, binName), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if v := m.View(); v.Failed == nil || v.Failed.Version != "v0.2.0" {
		t.Fatalf("应报出停用的新程序：%+v", v)
	}
	restart, err := m.Revert()
	if err != nil || restart {
		t.Fatalf("Revert = %v, %v", restart, err)
	}
	if _, err := os.Stat(filepath.Join(dir, binName)); !os.IsNotExist(err) {
		t.Fatalf("新程序应被删掉：%v", err)
	}
	select {
	case <-m.RestartRequested():
		t.Fatal("没在运行新程序，不该重启")
	default:
	}
}

// 下载地址只认 GitHub 的域名和 HTTPS；改 API 地址只能指向本机。
func TestAllowedHosts(t *testing.T) {
	m, err := New(Options{DataDir: t.TempDir(), Version: "v0.1.0", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for raw, want := range map[string]bool{
		"https://api.github.com/repos/x":                       true,
		"https://github.com/shiceai/agy2api/releases/download": true,
		"https://release-assets.githubusercontent.com/x":       true,
		"https://objects.githubusercontent.com/x":              true,
		"http://github.com/x":                                  false,
		"https://github.com:8443/x":                            false,
		"https://evil.example.com/x":                           false,
		"https://github.com.evil.example.com/x":                false,
		"https://raw.githubusercontent.com/x":                  false,
	} {
		u, _ := url.Parse(raw)
		if got := m.allowed(u); got != want {
			t.Errorf("allowed(%s) = %v，应为 %v", raw, got, want)
		}
	}
	for _, api := range []string{"https://example.com", "http://10.0.0.1:8080", "ftp://127.0.0.1"} {
		if _, err := New(Options{DataDir: t.TempDir(), Version: "v0.1.0", API: api}); err == nil {
			t.Errorf("API %s 应被拒绝", api)
		}
	}
}
