// Package fsatomic writes files atomically: a reader of the path
// observes either the previous content or the new content in full,
// never a torn write.
package fsatomic

import (
	"io/fs"
	"os"
)

// WriteFile writes data to path through a same-directory temp file and
// rename. The temp name is fixed (path + ".tmp"), so concurrent writers
// of one path must already be serialized by the caller.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
