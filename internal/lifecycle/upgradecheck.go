package lifecycle

import (
	"context"
	"errors"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
	"github.com/PublicAI01/trajector-cli/internal/semver"
)

// UpgradeCheckDetached is the argument that marks `hook upgrade-check`
// as the process that runs the check. Without it, the process is the
// one a session hook started, and all it does is start the one with
// it. Like the hook names, it is spelled into argv that one build
// hands another: an older build starts the check on whatever build is
// installed after it, so it never changes.
const UpgradeCheckDetached = "--detached"

// updatesCovered is the gate on the agreement: whether the one this
// device accepted tells the user about the automatic check. The tests
// stand in for an agreement that carries that section while none does
// yet (consent.UpdatesSince is empty); nothing else assigns it.
var updatesCovered = (*consent.Store).CoversUpdates

// upgradeCheckArgs is the argv a session hook starts the background
// check with, and upgradeCheckDetachedArgs the one that check starts.
// Both are constants: no input reaches the command line of a process
// this build starts.
var (
	upgradeCheckArgs         = claudesettings.HookArgv(claudesettings.HookUpgradeCheck)
	upgradeCheckDetachedArgs = claudesettings.HookArgv(claudesettings.HookUpgradeCheck, UpgradeCheckDetached)
)

// startUpgradeCheck starts the background check for a newer release
// when every gate on it passes, and otherwise starts nothing at all: a
// check that would only find it must not run is a process the user
// never agreed to. The gates are the user config (auto_upgrade), that
// the release pipeline built this binary, that the agreement the device
// accepted tells the user about the check, and that one is due. A
// device that signed out still checks: it accepted the agreement, and
// its sessions still go through trajector.
//
// It never waits for the check, and nothing of it reaches the hook's
// output: the process inherits none of the hook's streams.
func (m *Machine) startUpgradeCheck(ctx context.Context) {
	if !m.upgradeCheckDue() {
		return
	}
	// A check that did not start is the one the next session's hook
	// starts; the session itself is no reason to hear about it.
	_, _ = m.deps.Spawn(ctx, m.deps.ExecPath, upgradeCheckArgs, "")
}

// upgradeCheckDue reports whether every gate on the background check
// passes now.
func (m *Machine) upgradeCheckDue() bool {
	if !m.deps.AutoUpgrade || !m.deps.ReleaseBuild {
		return false
	}
	if covered, err := updatesCovered(m.consent); err != nil || !covered {
		return false
	}
	// A state file this build cannot read is due: the check replaces
	// it, and status says so in the meantime.
	state, err := selfupdate.LoadState(m.deps.Layout.UpgradeState())
	if err != nil {
		state = selfupdate.State{}
	}
	return state.Due(m.upgradeWindow())
}

// upgradeWindow is the moment a background check would run in. The
// service's refusal of this version, as the last upload recorded it,
// shortens the wait between checks: a device the service no longer
// takes uploads from has to reach a newer release soon.
func (m *Machine) upgradeWindow() selfupdate.Window {
	w := selfupdate.Window{Now: m.deps.Now()}
	if floor := m.handshake().MinClientVersion; floor != "" {
		order, ok := semver.Compare(m.deps.Version, floor)
		w.Behind = ok && order < 0
	}
	return w
}

// DetachUpgradeCheck starts the background check as a process of its
// own and returns. The process running it is the session hook's child,
// which Claude Code stops along with the hook when the hook runs past
// its time; the one it starts has a parent that is gone by then, so
// nothing stops it before it is done.
func (m *Machine) DetachUpgradeCheck(ctx context.Context) error {
	_, err := m.deps.Spawn(ctx, m.deps.ExecPath, upgradeCheckDetachedArgs, "")
	return err
}

// CheckForUpgrade is the background check itself: it runs
// selfupdate.Check on this installation and leaves the lines that need
// the user for their next command. Nothing else of it is said: what it
// found, and why it failed, are in the upgrade state file, which status
// and doctor show. Another upgrade running on the machine makes it a
// check that did nothing.
//
// The lines are left while the check still holds the upgrade lock, and
// the state file records them as told only once the notice store took
// them: a release a package manager has to install, and a finish no
// longer tried, are said nowhere else, so a store that cannot take them
// leaves them for the next check to tell.
func (m *Machine) CheckForUpgrade(ctx context.Context) {
	in := m.installation()
	in.Tell = func(checked selfupdate.Checked, err error) error {
		return m.notices.Add(m.checkNotices(checked, err)...)
	}
	_, err := selfupdate.Check(ctx, in, m.deps.Releases, m.upgradeWindow(), m.FinishUpgrade)
	// The binary now in place is not this build, so this process does
	// nothing more; the state file records the check as failed. The line
	// is a hint after the fact, and nothing waits on it being heard.
	if putBack, ok := errors.AsType[*selfupdate.PutBackError](err); ok {
		_ = m.notices.Add(putBackNotice(m.deps.Version, putBack.Version))
	}
}

// checkNotices is what a background check that came to checked, with
// err, has the user hear.
func (m *Machine) checkNotices(checked selfupdate.Checked, err error) []string {
	var lines []string
	if checked.FinishFailed {
		lines = append(lines, finishFailedNotice(m.deps.Version))
	}
	if checked.TellManaged {
		lines = append(lines, managedNotice(checked.Release, checked.Manager))
	}
	if unusable, ok := errors.AsType[*selfupdate.UnusableError](err); ok {
		lines = append(lines, doesNotStartNotice(unusable.Version, m.deps.Version))
	}
	return lines
}

// managedNotice tells the user of an installation a package manager
// owns that release is published, and the manager's command for it.
func managedNotice(release string, manager selfupdate.Manager) string {
	return "trajector " + release + " is available. Run `" + managerHandback[manager].command + "` to install it."
}

// doesNotStart says release was not installed because it does not
// start on this machine, so kept is still what runs. A manual upgrade
// says it as it is; the background check adds what to run.
func doesNotStart(release, kept string) string {
	return "trajector " + release + " does not start on this machine, so " + kept + " was kept."
}

// doesNotStartNotice is doesNotStart as the background check leaves it
// for the user's next command.
func doesNotStartNotice(release, kept string) string {
	return doesNotStart(release, kept) + " `trajector upgrade` tries again."
}

// putBackNotice tells the user that an upgrade to release stopped
// before it was done, and that kept, the build it replaced, is back in
// its place.
func putBackNotice(release, kept string) string {
	return "An upgrade to trajector " + release + " was interrupted, so " + kept + " was put back. `trajector upgrade` tries again."
}

// finishFailedNotice tells the user the install of release is done but
// what release does after its install is not, and where to look.
func finishFailedNotice(release string) string {
	return "The upgrade to " + release + " is installed but its setup did not finish. Run `trajector doctor --all`."
}
