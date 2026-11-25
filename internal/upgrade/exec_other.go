//go:build !unix

package upgrade

import "errors"

// execSupported 为假：Windows 不能把当前进程原地换成另一个程序，不支持在线升级。
const execSupported = false

func execve(string, []string, []string) error {
	return errors.ErrUnsupported
}
