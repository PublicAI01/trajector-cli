//go:build !windows

package follow

import (
	"errors"
	"os"
	"syscall"
)

// ProcessAlive reports whether pid names a running process. A process
// this user may not signal is still running, so a permission refusal
// counts as alive; only "no such process" counts as gone.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
