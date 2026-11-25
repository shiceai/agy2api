//go:build unix

package upgrade

import "syscall"

// execSupported 表示能把当前进程原地换成另一个程序（进程号不变，不依赖 Docker、systemd 的重启策略）。
const execSupported = true

func execve(path string, args, env []string) error {
	return syscall.Exec(path, args, env)
}
