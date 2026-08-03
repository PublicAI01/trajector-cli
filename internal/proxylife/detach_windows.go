package proxylife

import "syscall"

// detachedProcess is DETACHED_PROCESS, absent from the syscall package.
const detachedProcess = 0x00000008

func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
		HideWindow:    true,
	}
}
