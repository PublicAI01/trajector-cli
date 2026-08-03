//go:build !windows

package proxylife

import "syscall"

func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
