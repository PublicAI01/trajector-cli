//go:build !windows

package fsatomic

import "os"

// replaceCollision reports transient open/rename collisions, which only
// Windows produces: POSIX renames never contend with open handles.
func replaceCollision(error) bool { return false }

func openShared(path string) (*os.File, error) { return os.Open(path) }

func renameReplace(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }
