package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/semver"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// Sibling-name markers. Every transient file this package creates next
// to the installed binary spells the binary's own name first and one of
// these after it, so a leftover is always recognizable as one and never
// as the binary itself.
const (
	incomingMarker = ".incoming-"
	oldMarker      = ".old-"
	retiredMarker  = ".retired-"
)

// executablePerm is what an installed binary is left readable and
// runnable as: everyone may run it, only its owner may rewrite it.
const executablePerm = 0o755

// install puts binary, the release of version, at in.ExecPath, which is
// normally the running program's own path. The new content is staged
// as a sibling of the binary — same directory, so the move into place
// is a rename within one filesystem — run once to check it starts as
// that version, and only then swapped in, with the binary it replaces
// kept beside it. The build now in place is then asked to accept the
// install (verify): if it does, the install is committed (commit); if
// not, the kept binary goes back to its own name. Every failure leaves
// the previous binary in place and runnable: a machine that cannot be
// upgraded must still be a machine that works.
func install(ctx context.Context, in Installation, version string, binary []byte) error {
	execPath := in.ExecPath
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
	// The staged name ends as the binary's own does, so the system that
	// runs only files named like programs runs this one too.
	base := filepath.Base(execPath)
	f, err := os.CreateTemp(dir, base+incomingMarker+"*"+filepath.Ext(base))
	if err != nil {
		return fmt.Errorf("selfupdate: cannot write to %s: %w", dir, err)
	}
	// CreateTemp only reserves the name here; the content goes through
	// fsatomic below. Reserving by creating rather than by checking
	// whether a name is free means no leftover that a sweep could not
	// remove is ever written over.
	staged := f.Name()
	err = f.Close()
	if err == nil {
		// Through fsatomic, not a plain write: it flushes the content to
		// stable storage before the rename that installs it, and the mode
		// travels with the content rather than being left to the creation
		// mask, so the install is runnable whatever umask the upgrade ran
		// under. A staged file nobody flushed can read back empty after a
		// crash, and by then the rename has already unlinked the binary
		// that would have retried the upgrade. 2026-09-16.
		err = fsatomic.WriteFile(staged, binary, executablePerm)
	}
	if err != nil {
		os.Remove(staged)
		return fmt.Errorf("selfupdate: staging the new binary: %w", err)
	}
	if err := smoke(ctx, staged, version); err != nil {
		os.Remove(staged)
		return err
	}
	kept := keptName(execPath, in.Version)
	if err := replaceKeeping(staged, execPath, kept); err != nil {
		os.Remove(staged)
		return err
	}
	// From here until the install is committed or undone, a supervisor
	// that restarts a crashed proxy starts it from the new build. If
	// that build cannot serve, the restarted proxy fails until the kept
	// binary is back, a matter of seconds; nothing guards against it.
	if err := verify(ctx, execPath, version); err != nil {
		if back := os.Rename(kept, execPath); back != nil {
			// Not an *UnusableError: that one says the previous binary
			// is in place, and here the build that refused itself is.
			// So nothing is recorded against the release either, and
			// its reason is quoted without the claim that nothing was
			// installed.
			why := err.Error()
			if unusable, ok := errors.AsType[*UnusableError](err); ok {
				why = unusable.Reason
			}
			return fmt.Errorf("selfupdate: trajector %s was put in place and did not accept it (%s), and the previous binary could not be put back: %w; it is at %s", version, why, back, kept)
		}
		return err
	}
	commit(ctx, in, version, kept)
	return nil
}

// keptName is where the binary at execPath, which announces version,
// is kept while the build replacing it is checked. The name ends as the
// binary's own does, so a Windows user who finds it can still run it.
func keptName(execPath, version string) string {
	return execPath + oldMarker + version + filepath.Ext(execPath)
}

// execPathRetired is where a kept binary that could not be deleted when
// its successor's install was committed waits for a sweep to delete it.
func execPathRetired(execPath, version string) string {
	return execPath + retiredMarker + version + filepath.Ext(execPath)
}

// keptVersion is the version a kept binary named name announces, for a
// name keptName gives the binary called base. Anything else beginning
// with the kept marker is not one: on Windows, the builds before this
// transaction stepped the running binary aside under a random name
// after base+oldMarker, and that binary's install was committed.
func keptVersion(base, name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, base+oldMarker)
	if !ok {
		return "", false
	}
	if rest, ok = strings.CutSuffix(rest, filepath.Ext(base)); !ok || !semver.Comparable(rest) {
		return "", false
	}
	return rest, true
}

// replaceKeeping moves staged onto execPath and keeps what was there
// as kept. If the move fails, the binary is at execPath and nothing is
// at kept.
func replaceKeeping(staged, execPath, kept string) error {
	inPlace, err := keepAside(execPath, kept)
	if err != nil {
		return fmt.Errorf("selfupdate: keeping the previous binary beside %s: %w", execPath, err)
	}
	if err := os.Rename(staged, execPath); err != nil {
		if inPlace {
			os.Remove(kept)
		} else if back := os.Rename(kept, execPath); back != nil {
			return fmt.Errorf("selfupdate: installing over %s failed (%w) and the previous binary is at %s", execPath, err, kept)
		}
		return fmt.Errorf("selfupdate: installing over %s: %w", execPath, err)
	}
	return nil
}

// commit makes the install of version final, in an order that leaves
// a trace wherever it stops. The state file first names version as
// still to be finished; then the kept binary goes, and from that moment
// the new build is the installation, which the state file then names
// as installed; then the new build finishes its own install, and only
// when it says it has does the state file stop naming version as
// unfinished. A finish that fails, or a machine that stops before it
// ran, leaves the name there for the new build to finish later.
//
// A state file that cannot be written does not stop the commit: the
// new build has already accepted the install, and refusing it over a
// note would put back a binary nothing is wrong with.
func commit(ctx context.Context, in Installation, version, kept string) {
	_ = updateState(in.State, func(s *State) { s.FinishPending = version })
	// A kept binary is what a sweep puts back, so it must not outlive
	// the commit under its kept name. On Windows it cannot be deleted
	// while it is still the image of a running process — the one running
	// this upgrade, at least — but it can be renamed, as it was a moment
	// ago, to a name the next upgrade's sweep only deletes.
	if os.Remove(kept) != nil {
		_ = os.Rename(kept, execPathRetired(in.ExecPath, in.Version))
	}
	_ = updateState(in.State, func(s *State) { s.Installed = version })
	if finish(ctx, in.ExecPath) == nil {
		_ = updateState(in.State, func(s *State) { s.FinishPending = "" })
	}
}

// SweepResidue undoes or removes what an earlier upgrade left behind.
// A kept binary means that upgrade died between putting the new build
// in place and committing it — the commit deletes the kept binary
// before anything else can stop it — so the build at in's path never
// accepted its own install, and a build that has not accepted its own
// install must not stay in place: the kept binary goes back under its
// own name. Nothing is recorded against the build it
// replaces, which was never judged, and the next upgrade tries it
// again. Beside the binary, a staged binary that never landed and one
// retired by a committed upgrade are removed; beside the state file, a
// directory a new build's check of itself was stopped in. Anything
// still held open is left for the next sweep. This is housekeeping, not
// a diagnosis: it reports nothing and fails at nothing.
//
// It runs only under the upgrade lock, and does not wait for it: what
// sits beside the binary while another upgrade holds the lock is that
// upgrade's work in progress, and this sweep leaves it to that upgrade
// and to the next sweep. Upgrade sweeps under the lock it already holds,
// before it does anything else; this is the entry point for doctor,
// which tidies the same residue on a run that installs nothing — the
// user never had to know an upgrade was interrupted.
func SweepResidue(in Installation) {
	unlock, err := takeLock(in.Lock, 0)
	if err != nil {
		return
	}
	defer unlock()
	sweepResidue(in)
}

// sweepResidue is SweepResidue for a caller already holding the lock,
// which is what proves the upgrade that left the residue is dead. It
// returns the version of the binary it put back, or empty when it put
// none back: after a put back, the binary at in's path no longer
// announces in.Version.
func sweepResidue(in Installation) (putBack string) {
	base := filepath.Base(in.ExecPath)
	spare, putBack := putBackKept(in)
	removeEntries(filepath.Dir(in.ExecPath), os.Remove, func(name string) bool {
		if name == spare {
			return false
		}
		for _, marker := range []string{incomingMarker, oldMarker, retiredMarker} {
			if strings.HasPrefix(name, base+marker) {
				return true
			}
		}
		return false
	})
	// A check's directory holds the directories its proxy made, so it
	// goes with everything in it.
	removeEntries(filepath.Dir(in.State), os.RemoveAll, func(name string) bool {
		return strings.HasPrefix(name, userdirs.UpgradeVerifyPrefix)
	})
	return putBack
}

// putBackKept puts the binary an interrupted upgrade kept back at in's
// path. There is only ever one, as one upgrade at a time keeps one;
// should there be more, the one written last goes back and the rest
// are left to be removed as residue. When it cannot be put back — on
// Windows, while the build in its place is a running image — its name
// is returned as spare, so the sweep spares it for the next one:
// removing it would leave the unaccepted build installed for good. When
// it is put back, putBack is the version it announces.
func putBackKept(in Installation) (spare, putBack string) {
	dir, base := filepath.Dir(in.ExecPath), filepath.Base(in.ExecPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}
	var latest, version string
	var latestTime time.Time
	for _, e := range entries {
		v, ok := keptVersion(base, e.Name())
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if latest == "" || info.ModTime().After(latestTime) {
			latest, version, latestTime = e.Name(), v, info.ModTime()
		}
	}
	if latest == "" {
		return "", ""
	}
	if os.Rename(filepath.Join(dir, latest), in.ExecPath) != nil {
		return latest, ""
	}
	// The interrupted upgrade may have named its build as still to be
	// finished, after that build accepted the install and before the
	// kept binary went; the build put back is not that one.
	_ = updateState(in.State, func(s *State) {
		if s.FinishPending != version {
			s.FinishPending = ""
		}
	})
	return "", version
}

// removeEntries removes, with remove, every entry of dir whose name is
// residue.
func removeEntries(dir string, remove func(string) error, residue func(name string) bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		// One still held open stays for the next sweep.
		if residue(e.Name()) {
			_ = remove(filepath.Join(dir, e.Name()))
		}
	}
}
