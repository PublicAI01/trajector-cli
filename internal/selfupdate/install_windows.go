//go:build windows

package selfupdate

import "os"

// keepAside renames the binary at execPath to kept. Windows refuses to
// overwrite the image of a running program but allows it to be renamed,
// and the image stays mapped under its new name; so the binary is
// stepped aside rather than linked, and execPath is empty for the
// moment until the new binary takes its name. The stepped-aside file
// cannot be deleted while it is still a running process's image: when
// the install is committed it is left for the next upgrade's sweep.
func keepAside(execPath, kept string) (stillInPlace bool, err error) {
	return false, os.Rename(execPath, kept)
}
