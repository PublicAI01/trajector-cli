// Package selfupdate moves this installation to a newer published
// release. One call answers the whole question: whether a package
// manager owns the binary, whether this build has a place in the
// release order at all, which release is the newest published one, and
// — when there is one to move to — fetching it, checking it against
// its published checksum, running it once, swapping it in, and having
// the new build accept the install by serving here. It decides nothing
// the user reads: the Outcome carries the facts and the caller writes
// every sentence. A binary it has not verified, or one that does not
// start or serve here, is never left installed, and every exit that
// installs nothing leaves the previous binary in place and runnable.
//
// One upgrade at a time runs on a machine, under a lock in the user's
// state directory, and only the holder of that lock writes the upgrade
// state file (State, LoadState).
package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/semver"
)

// DefaultReleasesURL is where published releases are listed. The
// /releases/latest endpoint is deliberately not used: it omits
// pre-releases, and every 0.x release is published as one.
const DefaultReleasesURL = "https://api.github.com/repos/PublicAI01/trajector-cli/releases"

// Kind is which of the four things an upgrade run did. Every run that
// returns no error did exactly one of them.
type Kind int

const (
	// Managed: a package manager owns this installation and nothing was
	// touched. Overwriting a managed binary would be undone by the
	// manager's next command, or left as a version its records disagree
	// with, so the installation is handed back to its manager.
	Managed Kind = iota
	// NotARelease: this build announces a version no release order
	// contains — a build from a checkout — so nothing published can be
	// called newer than it, and replacing it would silently discard
	// whatever it was built to test.
	NotARelease
	// AlreadyNewest: nothing published is newer than what is installed.
	AlreadyNewest
	// Upgraded: the binary was replaced with the release named by To.
	Upgraded
)

// Outcome is what one upgrade run did, and the facts a caller needs to
// say so.
type Outcome struct {
	// Kind is which of the four things happened.
	Kind Kind
	// Manager owns this installation. Set only when Kind is Managed.
	Manager Manager
	// From is the version this build announces, whatever became of it.
	From string
	// To is the release now installed. Set only when Kind is Upgraded.
	To string
}

// Installation is one machine's trajector as an upgrade sees it: the
// binary to replace, the version that binary announces, and where the
// machine keeps the bookkeeping that lets one upgrade run at a time.
type Installation struct {
	// ExecPath is the binary an upgrade replaces, normally the running
	// program's own path.
	ExecPath string
	// Version is what the binary at ExecPath announces.
	Version string
	// State is the upgrade state file (userdirs Layout.UpgradeState).
	// The build being installed checks itself in a directory it makes
	// beside it (userdirs Layout.NewUpgradeVerifyDir), and the sweep
	// looks there for any it left.
	State string
	// Lock is the path whose lock one upgrade at a time holds (userdirs
	// Layout.UpgradeLock). It must not be State: the state file is
	// rewritten under a lock of its own while this one is held.
	Lock string
}

// ErrBusy reports that another upgrade on this machine held the upgrade
// lock for the whole wait. Nothing was fetched, recorded or changed.
var ErrBusy = errors.New("selfupdate: another upgrade is running on this machine")

// UnusableError reports a release that was fetched and verified but
// does not start on this machine. It was not installed, the binary at
// ExecPath is byte for byte what it was, and the release is recorded in
// the state file's unusable versions.
type UnusableError struct {
	// Version is the release that does not start.
	Version string
	// Reason is one line saying what running it did instead.
	Reason string
}

func (e *UnusableError) Error() string {
	return fmt.Sprintf("selfupdate: trajector %s does not start on this machine (%s); nothing was installed", e.Version, e.Reason)
}

// PutBackError reports a run that found an earlier upgrade interrupted
// before the build it put in place accepted the install, and put the
// binary that upgrade kept back in its place. The binary at ExecPath
// then announces Version, not the version of the process running the
// upgrade, so the run did nothing else: anything it went on to decide
// would be decided for a build that is no longer installed. Nothing is
// recorded as installed, and nothing against the build taken out.
type PutBackError struct {
	// Version is the release the binary put back announces.
	Version string
}

func (e *PutBackError) Error() string {
	return fmt.Sprintf("an earlier upgrade stopped before the build it put in place accepted the install, so trajector %s was put back and nothing else was done", e.Version)
}

// Upgrade moves in to the newest release published at indexURL. It
// waits up to wait for another upgrade on this machine to finish, and
// returns ErrBusy when that one still holds the lock: a manual upgrade
// waits a moment, a background one not at all.
//
// Which release that is comes from the release index alone: nothing
// else — no service, no argument — names what a machine upgrades to.
// The archive is checked against the release's published checksum, and
// the binary inside it has to answer `version` with the release's own
// version, before anything is replaced; once it is in place, the new
// build itself has to accept the install by serving on this machine,
// or the previous binary goes back. So a run that returns an error has
// left the machine exactly as it found it, minus whatever residue an
// interrupted earlier upgrade had left behind — unless the previous
// binary could not be put back, which the error says, with where that
// binary is. A committed
// install whose new build then fails to finish it still returns
// Upgraded: the state file names the version as unfinished
// (State.FinishPending) for the new build to finish later.
//
// A run that finds an earlier upgrade interrupted before its new build
// accepted the install puts back the binary that upgrade kept, and
// returns a *PutBackError having done nothing else.
//
// Every run that takes the lock records how it ended in the state file
// before letting go of it.
func Upgrade(ctx context.Context, in Installation, indexURL string, wait time.Duration) (Outcome, error) {
	unlock, err := takeLock(in.Lock, wait)
	if err != nil {
		return Outcome{}, err
	}
	defer unlock()

	out, err := upgrade(ctx, in, indexURL)
	// The record is a note for status and for the background checker,
	// not part of the installation: the binary on disk is what is
	// installed whatever the note says, and the next run that takes the
	// lock writes it afresh. The lock file was just created in the same
	// directory, so a record that cannot be written is a disk failing
	// mid-run, and reporting that over the run's own result would tell
	// the user an installed upgrade failed.
	_ = record(in, out, err)
	return out, err
}

// upgrade is one run under the upgrade lock.
func upgrade(ctx context.Context, in Installation, indexURL string) (Outcome, error) {
	// Housekeeping comes first, whether or not this run installs
	// anything: on Windows the binary an earlier upgrade stepped aside
	// could not be deleted while it was still the running image.
	if putBack := sweepResidue(in); putBack != "" {
		return Outcome{}, &PutBackError{Version: putBack}
	}

	if manager := packageManager(in.ExecPath); manager != "" {
		return Outcome{Kind: Managed, Manager: manager, From: in.Version}, nil
	}
	if !semver.Comparable(in.Version) {
		return Outcome{Kind: NotARelease, From: in.Version}, nil
	}

	goos, goarch := hostPlatform()
	src := newSource(indexURL, in.Version)
	rel, err := src.newest(ctx, goos, goarch)
	if err != nil {
		return Outcome{}, err
	}
	// A release that is not strictly newer is not an upgrade: a
	// withdrawn release, or a machine built from a tag ahead of the
	// source, must not be moved backwards.
	if order, ok := semver.Compare(rel.version, in.Version); !ok || order <= 0 {
		return Outcome{Kind: AlreadyNewest, From: in.Version}, nil
	}

	binary, err := src.download(ctx, rel, goos)
	if err != nil {
		return Outcome{}, err
	}
	if err := install(ctx, in, rel.version, binary); err != nil {
		return Outcome{}, err
	}
	return Outcome{Kind: Upgraded, From: in.Version, To: rel.version}, nil
}
