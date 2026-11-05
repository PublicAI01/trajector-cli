//go:build !windows

package userdirs

import "os"

func ensureOwnerDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}
