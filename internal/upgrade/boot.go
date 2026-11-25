package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 自带的程序换成新程序时设的环境变量：自带程序的版本和路径。新程序靠它们知道自己是在线升级来的，
// 重启时换回自带的程序，由它重新选择。
const (
	envBase    = "AGY2API_UPGRADE_BASE"
	envBaseExe = "AGY2API_UPGRADE_BASE_EXE"
)

// maxAttempts 是新程序确认能正常启动之前最多尝试的次数，再起不来就停用它，退回自带的程序。
const maxAttempts = 3

type bootAction int

const (
	bootSelf    bootAction = iota // 运行自己
	bootOverlay                   // 换成新程序
	bootCleanup                   // 删掉新程序，运行自己
	bootDisable                   // 停用新程序（连续起不来），运行自己
)

// decide 决定启动时运行哪个程序。own 是自己的版本。
func decide(st *State, own, goos, goarch string) bootAction {
	if st == nil || st.Failed {
		return bootSelf
	}
	ownRel, ok := parseRelease(own)
	if !ok {
		return bootSelf // 开发构建：不碰数据目录里的程序
	}
	stRel, ok := parseRelease(st.Version)
	if !ok || st.OS != goos || st.Arch != goarch || compare(ownRel, stRel) >= 0 {
		return bootCleanup
	}
	if !st.Confirmed && st.Attempts >= maxAttempts {
		return bootDisable
	}
	return bootOverlay
}

// Boot 在进程启动的最前面调用（日志已设置、还没打开数据库）：数据目录里有比自己新、可用的新程序时，
// 把当前进程换成它，成功不返回；自己已经不比它旧时删掉它。enabled 为假（AGY2API_UPDATE=off）时什么都不做。
func Boot(dataDir, version string, enabled bool) {
	if !enabled || !execSupported || os.Getenv(envBaseExe) != "" {
		return
	}
	dir := upgradeDir(dataDir)
	st, err := readState(dir)
	if err != nil {
		slog.Warn("读不了在线升级的记录，运行自带的程序", "err", err)
		return
	}
	switch decide(st, version, runtime.GOOS, runtime.GOARCH) {
	case bootSelf:
		return
	case bootCleanup:
		if err := removeOverlay(dir); err != nil {
			slog.Warn("清理在线升级的程序失败", "err", err)
			return
		}
		slog.Info("自带的程序已经不比在线升级的旧，清理在线升级的程序", "version", version, "removed", st.Version)
		return
	case bootDisable:
		disable(dir, st, fmt.Sprintf("连续 %d 次没能启动", maxAttempts))
		return
	}

	bin := filepath.Join(dir, binName)
	if sum, err := fileSHA256(bin); err != nil || sum != st.SHA256 {
		disable(dir, st, "程序文件和记录不符")
		return
	}
	if !st.Confirmed {
		st.Attempts++
		if err := writeState(dir, st); err != nil {
			slog.Error("写不了在线升级的记录，运行自带的程序", "err", err)
			return
		}
	}
	exe, err := os.Executable()
	if err != nil {
		slog.Error("找不到自带程序的路径，运行自带的程序", "err", err)
		return
	}
	slog.Info("换成在线升级的程序", "version", st.Version, "base", version)
	env := append(withoutUpgradeEnv(os.Environ()), envBase+"="+version, envBaseExe+"="+exe)
	err = execve(bin, os.Args, env)
	slog.Error("换成在线升级的程序失败，运行自带的程序", "err", err)
}

// disable 停用新程序并记下原因，文件留着方便排查，下次在线升级时覆盖。
func disable(dir string, st *State, reason string) {
	st.Failed, st.Reason = true, reason
	if err := writeState(dir, st); err != nil {
		slog.Error("写不了在线升级的记录", "err", err)
	}
	slog.Error("停用在线升级的程序，运行自带的程序", "version", st.Version, "reason", reason)
}

// Running 报告当前进程是不是在线升级的程序，是的话 base 是自带程序的版本。
func Running() (base string, ok bool) {
	if os.Getenv(envBaseExe) == "" {
		return "", false
	}
	return os.Getenv(envBase), true
}

// Confirm 在服务开始监听后调用：当前是在线升级的程序时记下它能正常启动，之后不再计启动次数。
func Confirm(dataDir string) {
	if _, ok := Running(); !ok {
		return
	}
	dir := upgradeDir(dataDir)
	st, err := readState(dir)
	if err != nil || st == nil || st.Confirmed {
		return
	}
	st.Confirmed, st.Attempts = true, 0
	if err := writeState(dir, st); err != nil {
		slog.Warn("写不了在线升级的记录", "err", err)
	}
}

// Restart 把当前进程换回自带的程序，由它重新选择运行哪个（在线升级后是新程序，撤销后是它自己），成功不返回。
// 调用前要关掉监听和数据库。
func Restart() error {
	exe := os.Getenv(envBaseExe)
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return err
		}
	}
	return execve(exe, os.Args, withoutUpgradeEnv(os.Environ()))
}

func withoutUpgradeEnv(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if strings.HasPrefix(kv, envBase+"=") || strings.HasPrefix(kv, envBaseExe+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
