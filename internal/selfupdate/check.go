package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/semver"
)

// How often the background check asks for the newest release: once a
// day, or once an hour while the service refuses this version as older
// than it accepts. Both are what PRIVACY.md tells the user.
const (
	checkInterval       = 24 * time.Hour
	checkIntervalBehind = time.Hour
)

// Window is the moment a background check would run in: the time it
// would run at, and whether the service has refused this version as
// older than the oldest it accepts, which shortens the wait between
// checks. What the service accepts is the caller's to read; this
// package never reads what the service said.
type Window struct {
	Now    time.Time
	Behind bool
}

// Due reports whether a background check is due in w: whether the wait
// since the last one has passed. It compares the time the last check
// recorded with w.Now and never measures time passing itself. A machine
// that never checked is due, and so is one whose last check is later
// than w.Now: its clock went back, and waiting for it to catch up would
// hold the check off for as long as it went back.
func (s *State) Due(w Window) bool {
	if s.CheckedAt.IsZero() || s.CheckedAt.After(w.Now) {
		return true
	}
	interval := checkInterval
	if w.Behind {
		interval = checkIntervalBehind
	}
	return w.Now.Sub(s.CheckedAt) >= interval
}

// Checked is what one background check came to, and the facts a caller
// needs to tell the user about it. The check decides nothing the user
// reads, as Upgrade does not.
type Checked struct {
	// Result is what the check came to, as the state file now records
	// it. It is empty when the check was not due after all — another
	// one finished inside the window first — or when the time of the
	// check could not be recorded; nothing was asked either way.
	Result Result
	// Release is the newest published release when it is newer than
	// this build: the one the check installed, the one a package
	// manager has to install, or the one it did not install. It is
	// empty when no newer release was found.
	Release string
	// Manager owns this installation. Set only when Result is
	// ResultManaged.
	Manager Manager
	// TellManaged reports that Release was not reported for this
	// managed installation before. Once the user is told
	// (Installation.Tell), the state file records it as reported, so no
	// later check reports it again.
	TellManaged bool
	// FinishFailed reports that finishing this build's own install has
	// now failed twice in a row, the last time in this check. Once the
	// user is told (Installation.Tell), it is no longer tried: the state
	// file stops naming it unfinished.
	FinishFailed bool
}

// tells reports whether c, which came with err, has something the user
// has to hear: a release a package manager has to install, a finish no
// longer tried, or a release this check found does not start here.
func (c Checked) tells(err error) bool {
	_, unusable := errors.AsType[*UnusableError](err)
	return c.TellManaged || c.FinishFailed || unusable
}

// errNotARelease is a background check on a build no release order
// contains. Nothing published can be called newer than such a build.
var errNotARelease = errors.New("this build reports a version that is not a published release")

// Check is the background check for a newer release: the one run on
// its own, with nobody waiting for it and nobody to ask.
//
// It takes the upgrade lock without waiting, and returns ErrBusy when
// another upgrade holds it. Under the lock it judges again whether the
// check is due in w: a check started just as another one finished finds
// the state file already up to date, and then returns the zero Checked
// having asked nothing.
//
// A due check first sweeps what an earlier upgrade left behind. A sweep
// that puts back the binary an interrupted upgrade kept ends the check
// there, with a *PutBackError the state file records as its failure.
// Otherwise the check records its time before it asks anything: a state
// file that cannot take it ends the check with that error, having asked
// nothing, so a machine whose state file cannot be written does not ask
// on every session.
//
// A build whose own install is still unfinished — the state file names
// its version as State.FinishPending, because its finish failed or the
// machine stopped before it ran — runs finish again first, in this
// process; finish is this build's own. A second failure in a row is the
// last: Checked says so for the caller to send the user to what repairs
// it by hand, and once the user is told the version stops being named
// unfinished.
//
// The check then asks the release index at indexURL for the newest
// release and does one of four things, which the state file records
// with the time of w: nothing, when this build is the newest; reports
// the release, when a package manager owns this installation, which is
// never replaced (once per release); installs it the way Upgrade does;
// or fails. A release recorded as one that does not start on this
// machine is not tried again until a newer one is published: the check
// fails with the reason it was recorded with. Upgrade tries it again,
// because the user asked for it.
//
// The error, when there is one, is also what the state file now
// records. An *UnusableError means the release was tried here, in this
// check, and did not start.
func Check(ctx context.Context, in Installation, indexURL string, w Window, finish func(context.Context) error) (Checked, error) {
	unlock, err := takeLock(in.Lock, 0)
	if err != nil {
		return Checked{}, err
	}
	defer unlock()

	// A state file this build cannot read is due: the record below
	// replaces it, and a check held off by a note nobody can read would
	// never run again.
	s, err := LoadState(in.State)
	if err != nil {
		s = State{}
	}
	if !s.Due(w) {
		return Checked{}, nil
	}

	// The sweep comes before anything this build does as the installed
	// one. An interrupted upgrade may have left in place a build that
	// never accepted its install; once the binary it kept is back, this
	// process is no longer the installation, and finishing its install
	// or asking for a release newer than it would be done for a build
	// that is gone.
	if putBack := sweepResidue(in); putBack != "" {
		err := &PutBackError{Version: putBack}
		_ = updateState(in.State, func(s *State) {
			s.CheckedAt, s.Result = w.Now, ResultFailed
			s.noteRun(in, "", err)
		})
		return Checked{Result: ResultFailed}, err
	}
	// The time of the check is what holds the next one off, so it is
	// recorded before the release index is asked. A state file that
	// cannot take it would let every session ask again; the check asks
	// nothing instead.
	if err := updateState(in.State, func(s *State) { s.CheckedAt = w.Now }); err != nil {
		return Checked{}, fmt.Errorf("recording the time of the check: %w", err)
	}

	finishFailed := false
	if s.FinishPending != "" && s.FinishPending == in.Version {
		// As in commit, the note is not the install: a state file that
		// cannot be written leaves the version named, and the next
		// check finishes it again, which adds nothing.
		if finish(ctx) == nil {
			_ = updateState(in.State, func(s *State) { s.setFinishPending("") })
		} else {
			_ = updateState(in.State, func(s *State) { finishFailed = s.finishFailed() })
		}
	}

	c, err := check(ctx, in, indexURL, s)
	c.FinishFailed = finishFailed
	if err != nil {
		c.Result, c.Manager, c.TellManaged = ResultFailed, "", false
	}
	installed := ""
	if c.Result == ResultUpgraded {
		installed = c.Release
	}
	told := true
	if in.Tell != nil && c.tells(err) {
		told = in.Tell(c, err) == nil
	}
	// For the reason Upgrade gives: the record is a note, not the
	// installation, and the next check that takes the lock writes it
	// afresh.
	_ = updateState(in.State, func(s *State) {
		s.CheckedAt, s.Result = w.Now, c.Result
		s.noteRun(in, installed, err)
		if !told {
			return
		}
		if c.TellManaged {
			s.ToldManaged = c.Release
		}
		// An install committed in this check names a version of its
		// own, which is not the one given up on.
		if c.FinishFailed && s.FinishPending == in.Version {
			s.setFinishPending("")
		}
	})
	return c, err
}

// check is one background check under the upgrade lock, on the state
// the lock holder read.
func check(ctx context.Context, in Installation, indexURL string, s State) (Checked, error) {
	if !semver.Comparable(in.Version) {
		return Checked{}, errNotARelease
	}
	goos, goarch := hostPlatform()
	src := newSource(indexURL, in.Version)
	rel, err := src.newest(ctx, goos, goarch)
	if err != nil {
		return Checked{}, err
	}
	if !newer(rel.version, in.Version) {
		return Checked{Result: ResultNewest}, nil
	}

	c := Checked{Release: rel.version}
	if manager := packageManager(in.ExecPath); manager != "" {
		c.Result, c.Manager, c.TellManaged = ResultManaged, manager, s.ToldManaged != rel.version
		return c, nil
	}
	if reason, ok := s.Unusable[rel.version]; ok {
		return c, fmt.Errorf("trajector %s does not start on this machine (%s); it is not tried again until a newer release is published", rel.version, reason)
	}
	if err := installRelease(ctx, in, src, rel, goos); err != nil {
		return c, err
	}
	c.Result = ResultUpgraded
	return c, nil
}
