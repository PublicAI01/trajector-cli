//go:build !windows

package fsatomic

import (
	"io/fs"
	"os"
)

func createTemp(dir, prefix string) (*os.File, error) { return os.CreateTemp(dir, prefix+"*") }

func setTempPermissions(tmp, _ string, perm fs.FileMode) error { return os.Chmod(tmp, perm) }

// replaceCollision reports transient open/rename collisions, which only
// Windows produces: POSIX renames never contend with open handles.
func replaceCollision(error) bool { return false }

func openShared(path string) (*os.File, error) { return os.Open(path) }

func renameReplace(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// flushDir makes a completed rename durable by flushing the directory
// that now names the file: opening the directory read-only and syncing
// the handle is the portable POSIX way to do that.
func flushDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
