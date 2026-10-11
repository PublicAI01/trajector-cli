//go:build !windows

package follow

import (
	"io/fs"
	"syscall"
)

func inodeOf(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
