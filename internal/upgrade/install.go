package upgrade

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// 发布包、程序、校验和文件的大小上限，防止异常的响应写满磁盘。
const (
	maxPackage = 200 << 20
	maxBinary  = 300 << 20
	maxSums    = 64 << 10
)

// install 下载这个版本当前平台的发布包，按 SHA256SUMS.txt 校验，取出程序并运行一次确认版本，
// 备份数据库后放进数据目录、写好记录。
func (m *Manager) install(ctx context.Context, rel *Release) error {
	dir := upgradeDir(m.opts.DataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := fmt.Sprintf("agy2api_%s_%s_%s", strings.TrimPrefix(rel.Version, "v"), runtime.GOOS, runtime.GOARCH)
	pkg, ok := rel.assets[name+".tar.gz"]
	if !ok {
		return fmt.Errorf("%s 没有 %s/%s 的发布包", rel.Version, runtime.GOOS, runtime.GOARCH)
	}
	sums, ok := rel.assets["SHA256SUMS.txt"]
	if !ok {
		return fmt.Errorf("%s 缺少 SHA256SUMS.txt，无法校验", rel.Version)
	}

	dlCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	want, err := m.expectedSum(dlCtx, sums, name+".tar.gz")
	if err != nil {
		return err
	}
	archive := filepath.Join(dir, "download.tar.gz")
	defer os.Remove(archive)
	got, err := m.download(dlCtx, pkg, archive)
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("下载的发布包和 SHA256SUMS.txt 对不上，可能不完整或被篡改")
	}

	m.setProgress(func(p *Progress) { p.State = StateVerifying })
	tmp := filepath.Join(dir, binName+".new")
	defer os.Remove(tmp) // 改名成功后这里删不到东西
	sum, err := extractBinary(archive, name+"/"+binName, tmp)
	if err != nil {
		return err
	}
	if v, err := m.runVersion(ctx, tmp); err != nil {
		return err
	} else if v != rel.Version {
		return fmt.Errorf("下载的程序报告的版本是 %q，不是 %s", v, rel.Version)
	}

	m.setProgress(func(p *Progress) { p.State = StateBackingUp })
	if err := m.backup(ctx, rel.Version); err != nil {
		return err
	}

	if err := os.Rename(tmp, filepath.Join(dir, binName)); err != nil {
		return err
	}
	return writeState(dir, &State{
		Version:     rel.Version,
		SHA256:      sum,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		InstalledAt: time.Now().UnixMilli(),
		From:        m.opts.Version,
	})
}

// expectedSum 从 SHA256SUMS.txt 里找 file 的摘要（sha256sum 的输出格式，文件名前可能带 *）。
func (m *Manager) expectedSum(ctx context.Context, sums asset, file string) (string, error) {
	resp, err := m.get(ctx, sums.url)
	if err != nil {
		return "", fmt.Errorf("下载 SHA256SUMS.txt 失败：%w", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxSums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == file {
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("读 SHA256SUMS.txt 失败：%w", err)
	}
	return "", fmt.Errorf("SHA256SUMS.txt 里没有 %s", file)
}

// download 把发布包下载到 dst，边下边算 SHA256、更新进度。
func (m *Manager) download(ctx context.Context, a asset, dst string) (string, error) {
	if a.size <= 0 || a.size > maxPackage {
		return "", fmt.Errorf("发布包大小不对：%d 字节", a.size)
	}
	m.setProgress(func(p *Progress) { p.Total = a.size })
	resp, err := m.get(ctx, a.url)
	if err != nil {
		return "", fmt.Errorf("下载发布包失败：%w", err)
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h, progressWriter{m}), io.LimitReader(resp.Body, a.size+1))
	if err != nil {
		return "", fmt.Errorf("下载发布包失败：%w", err)
	}
	if n != a.size {
		return "", fmt.Errorf("发布包下载不完整：%d / %d 字节", n, a.size)
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type progressWriter struct{ m *Manager }

func (w progressWriter) Write(p []byte) (int, error) {
	w.m.setProgress(func(pr *Progress) { pr.Received += int64(len(p)) })
	return len(p), nil
}

// extractBinary 从发布包里取出 member（发布包里的程序）写到 dst，返回它的 SHA256。
// 只写 dst 这一个固定路径，不按包里的路径落盘。
func extractBinary(archive, member, dst string) (string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("发布包解不开：%w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("发布包里没有 %s", member)
		}
		if err != nil {
			return "", fmt.Errorf("发布包解不开：%w", err)
		}
		if strings.TrimPrefix(hdr.Name, "./") != member {
			continue
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Size <= 0 || hdr.Size > maxBinary {
			return "", fmt.Errorf("发布包里的 %s 不是正常的程序文件", member)
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
		if err != nil {
			return "", err
		}
		h := sha256.New()
		_, err = io.Copy(io.MultiWriter(out, h), io.LimitReader(tr, hdr.Size))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", fmt.Errorf("取出程序失败：%w", err)
		}
		if err := os.Chmod(dst, 0o700); err != nil { // 不受 umask 影响
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
}

// runVersion 运行下载的程序取版本号，同时确认它能在这台机器上运行。
func runVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return "", fmt.Errorf("下载的程序在这台机器上运行不了：%w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// backup 把数据库备份到 backups/agy2api-before-<版本>.db，成功后删掉更早的备份，只留这一份。
func (m *Manager) backup(ctx context.Context, version string) error {
	if m.opts.Backup == nil {
		return nil
	}
	dst := backupPath(m.opts.DataDir, version)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := m.opts.Backup(ctx, dst); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("备份数据库失败：%w", err)
	}
	olds, _ := filepath.Glob(filepath.Join(filepath.Dir(dst), backupGlob))
	for _, p := range olds {
		if p != dst {
			_ = os.Remove(p)
		}
	}
	return nil
}
