//go:build windows

package follow

import "os"

// ProcessAlive reports whether pid names a running process. On
// Windows, finding a process opens a handle to it, which fails for a
// process that is gone.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}
