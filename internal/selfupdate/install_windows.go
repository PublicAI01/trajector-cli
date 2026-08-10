//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
)

// replaceExecutable installs staged at execPath on a system that
// refuses to overwrite the image of a running program. The running
// binary is renamed aside first — which Windows does allow, the image
// stays mapped under the new name — the new one takes the vacated
// name, and the stepped-aside file is deleted if it can be. It cannot
// be while it is still this process's own image, so it is left for
// SweepResidue on the next run.
//
// If either move fails, the previous binary goes back to its own name
// before the error is returned: there is no ordering here that leaves
// execPath missing.
func replaceExecutable(staged, execPath string) error {
	aside, err := reserveAside(execPath)
	if err != nil {
		return err
	}
	if err := os.Rename(execPath, aside); err != nil {
		os.Remove(aside)
		return fmt.Errorf("selfupdate: moving the running %s aside: %w", execPath, err)
	}
	if err := os.Rename(staged, execPath); err != nil {
		if back := os.Rename(aside, execPath); back != nil {
			return fmt.Errorf("selfupdate: installing over %s failed (%w) and the previous binary is at %s", execPath, err, aside)
		}
		return fmt.Errorf("selfupdate: installing over %s: %w", execPath, err)
	}
	os.Remove(aside)
	return nil
}

// reserveAside picks an unused sibling name for the binary being
// stepped aside, by creating the file rather than by checking whether
// a name is free: two upgrades running at once must not choose one
// name between them.
func reserveAside(execPath string) (string, error) {
	dir := filepath.Dir(execPath)
	f, err := os.CreateTemp(dir, filepath.Base(execPath)+oldMarker+"*")
	if err != nil {
		return "", fmt.Errorf("selfupdate: cannot write to %s: %w", dir, err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("selfupdate: cannot write to %s: %w", dir, err)
	}
	return name, nil
}
