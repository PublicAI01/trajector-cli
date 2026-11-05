//go:build !windows

package follow

import (
	"io/fs"
	"os"
	"syscall"
)

func statFile(path string) (Stat, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Stat{}, err
	}
	return Stat{Exists: true, Inode: inodeOf(info), Size: info.Size()}, nil
}

func inodeOf(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
