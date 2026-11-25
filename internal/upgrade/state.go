// Package upgrade 是在线升级：查 GitHub Releases 上的新版本，下载、校验后把新程序放进数据目录，
// 原地重启换成它；进程启动时在自带的程序（镜像或安装包里的）和数据目录里的新程序中选较新的运行。
//
// 新程序放在数据目录而不是替换自带的程序：Docker 里容器重建会丢掉容器内的改动，compose 文件里写的镜像版本也还是旧的；
// 数据目录（Docker 的数据卷、systemd 的 StateDirectory）跨重建保留，systemd 的加固也不用放松。之后换上同版本或更新的
// 镜像、安装包时，自带的程序不再比它旧，启动时把它删掉。
package upgrade

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// 数据目录下的文件：upgrade/agy2api 是新程序，upgrade/state.json 是它的记录，
// backups/agy2api-before-<版本>.db 是升级前的数据库备份（只留最近一份）。
const (
	dirName    = "upgrade"
	binName    = "agy2api"
	stateName  = "state.json"
	backupDir  = "backups"
	backupGlob = "agy2api-before-*.db"
)

// State 是数据目录里新程序的记录。
type State struct {
	// Version 是新程序的版本，SHA256 是它的文件摘要，启动时核对。
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	// OS、Arch 是下载时的平台，数据目录搬到别的平台时不用它。
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// InstalledAt 是放进来的时间（Unix 毫秒），From 是当时运行的版本。
	InstalledAt int64  `json:"installed_at"`
	From        string `json:"from"`
	// Attempts 是确认能正常启动之前启动过的次数，Confirmed 表示已经正常启动过（监听成功）。
	Attempts  int  `json:"attempts"`
	Confirmed bool `json:"confirmed"`
	// Failed 表示不再使用它（连续启动失败、文件和记录不符），Reason 是原因。
	Failed bool   `json:"failed,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func upgradeDir(dataDir string) string { return filepath.Join(dataDir, dirName) }

// readState 读记录，没有时返回 nil。
func readState(dir string) (*State, error) {
	raw, err := os.ReadFile(filepath.Join(dir, stateName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", stateName, err)
	}
	return &st, nil
}

// writeState 先写临时文件再改名，中途断电也不会留下半份记录。
func writeState(dir string, st *State) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, stateName+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, stateName))
}

// removeOverlay 删掉新程序和它的记录（备份保留）。
func removeOverlay(dir string) error {
	err1 := os.Remove(filepath.Join(dir, binName))
	err2 := os.Remove(filepath.Join(dir, stateName))
	if errors.Is(err1, fs.ErrNotExist) {
		err1 = nil
	}
	if errors.Is(err2, fs.ErrNotExist) {
		err2 = nil
	}
	return errors.Join(err1, err2)
}
