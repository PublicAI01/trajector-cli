//go:build !windows

package lifecycle_test

import (
	"io/fs"
	"os"
	"testing"
)

func restrictDirectory(t *testing.T, dir string, mode fs.FileMode) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, info.Mode().Perm()); err != nil {
			t.Error(err)
		}
	})
}
