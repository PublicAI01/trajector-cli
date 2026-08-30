package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sibling-name markers. Every transient file this package creates next
// to the installed binary spells the binary's own name first and one of
// these after it, so a leftover is always recognizable as one and never
// as the binary itself.
const (
	incomingMarker = ".incoming-"
	oldMarker      = ".old-"
)

// executablePerm is what an installed binary is left readable and
// runnable as: everyone may run it, only its owner may rewrite it.
const executablePerm = 0o755

// install puts binary at execPath, which is normally the running
// program's own path. The new content is staged as a sibling of
// execPath — same directory, so the move into place is a rename within
// one filesystem — and only then swapped in. Every failure leaves the
// previous binary in place and runnable: a machine that cannot be
// upgraded must still be a machine that works.
func install(execPath string, binary []byte) error {
	// Whatever occupies execPath must be a file before anything is
	// staged over it. What a rename does when a directory stands there
	// is not the same everywhere — POSIX refuses it, Windows can move
	// the directory aside and leave the binary in its place, taking the
	// directory with it — so the case is settled here, once, rather
	// than left to the platform. A path that holds nothing yet is fine:
	// staging then fails, or the install lands, on its own merits.
	if info, err := os.Stat(execPath); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("selfupdate: %s is not a file; nothing was installed over it", execPath)
	}
	dir := filepath.Dir(execPath)
	f, err := os.CreateTemp(dir, filepath.Base(execPath)+incomingMarker+"*")
	if err != nil {
		return fmt.Errorf("selfupdate: cannot write to %s: %w", dir, err)
	}
	staged := f.Name()
	_, err = f.Write(binary)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// The mode is applied explicitly rather than left to the
		// creation mask, so the installed binary is runnable no matter
		// what umask the upgrade ran under.
		err = os.Chmod(staged, executablePerm)
	}
	if err != nil {
		os.Remove(staged)
		return fmt.Errorf("selfupdate: staging the new binary: %w", err)
	}
	if err := replaceExecutable(staged, execPath); err != nil {
		os.Remove(staged)
		return err
	}
	return nil
}

// SweepResidue removes what an earlier upgrade left beside execPath:
// a staged binary that never landed, and — on Windows, where the
// running executable can only be renamed aside rather than overwritten
// — the previous binary under its stepped-aside name, which could not
// be deleted while it was still the running process. Anything still
// held open is left for the next sweep. This is housekeeping, not a
// diagnosis: it reports nothing and fails at nothing.
//
// Upgrade sweeps before it does anything else. It is a second entry
// point because doctor tidies the same residue on a run that installs
// nothing — the user never had to know an upgrade was interrupted.
func SweepResidue(execPath string) {
	dir := filepath.Dir(execPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	base := filepath.Base(execPath)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base+incomingMarker) &&
			!strings.HasPrefix(name, base+oldMarker) {
			continue
		}
		os.Remove(filepath.Join(dir, name))
	}
}
