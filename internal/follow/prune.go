package follow

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// gone reports whether the file at path is gone for good: the
// directory that held it is still there, and the file is not. It is
// the one judgment of that, for every caller that drops an entry for
// it. A directory that is gone as well says nothing about the file: a
// volume that is not mounted, or a configuration directory moved
// away, takes the directory with it, and the file may come back with
// it. A file or a directory that cannot be looked at is still there.
func gone(path string) bool {
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Lstat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// prunable reports whether f, with the heat of its session, is an
// entry nothing reads again: its session is cold at now and its file
// is gone. A hot entry is left to its reader, which drops it once it
// finds the file gone. An entry whose file is still there is never
// dropped: a hook that names the file again would register it from
// byte 0, and the segments read from there would take indexes already
// sent with other lines.
func (f File) prunable(now time.Time, alive func(pid int) bool) bool {
	return !f.Hot(now, alive) && gone(f.Path)
}
