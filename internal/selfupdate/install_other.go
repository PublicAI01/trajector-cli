//go:build !windows

package selfupdate

import (
	"fmt"
	"os"
)

// replaceExecutable moves staged onto execPath. A running program on
// these systems holds its executable by inode, not by name, so the
// replacement is a single rename: the process that called it keeps
// running the old image until it exits, and the next invocation gets
// the new one.
func replaceExecutable(staged, execPath string) error {
	if err := os.Rename(staged, execPath); err != nil {
		return fmt.Errorf("selfupdate: installing over %s: %w", execPath, err)
	}
	return nil
}
