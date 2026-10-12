//go:build !windows

package selfupdate

import "os"

// keepAside makes kept a second name for the binary at execPath, so
// that the rename which then installs the new binary over execPath is
// the single step it has always been: a hook starting at that instant
// finds one binary or the other there, never neither. A running
// program on these systems holds its executable by inode, not by name,
// so nothing that is running notices. Where the filesystem has no
// hard links the binary is renamed aside instead, and execPath is
// empty for the moment until the new binary takes its name.
// stillInPlace reports which of the two happened.
func keepAside(execPath, kept string) (stillInPlace bool, err error) {
	if os.Link(execPath, kept) == nil {
		return true, nil
	}
	return false, os.Rename(execPath, kept)
}
