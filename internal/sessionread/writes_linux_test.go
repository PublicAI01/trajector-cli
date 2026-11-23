//go:build linux

package sessionread

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

// registryWrites counts, from now on, the writes that replace the file
// at path, and reports that it could count them: a write through
// fsatomic renames a finished file onto the name. The directory must
// exist.
func registryWrites(t *testing.T, path string) func() (n int, counted bool) {
	t.Helper()
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if _, err := syscall.InotifyAddWatch(fd, filepath.Dir(path), syscall.IN_MOVED_TO|syscall.IN_CLOSE_WRITE); err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(path)
	count := 0
	buf := make([]byte, 64<<10)
	return func() (int, bool) {
		t.Helper()
		for {
			n, err := syscall.Read(fd, buf)
			if errors.Is(err, syscall.EAGAIN) {
				return count, true
			}
			if err != nil {
				t.Fatal(err)
			}
			for off := 0; off+syscall.SizeofInotifyEvent <= n; {
				size := int(binary.NativeEndian.Uint32(buf[off+12:]))
				at := off + syscall.SizeofInotifyEvent
				if string(bytes.TrimRight(buf[at:at+size], "\x00")) == name {
					count++
				}
				off = at + size
			}
		}
	}
}
