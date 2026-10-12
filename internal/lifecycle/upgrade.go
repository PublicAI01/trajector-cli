package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// managerHandback is how to move an installation its package manager
// owns: the manager's name as the user knows it, and the command that
// does the upgrade. One entry per manager selfupdate can recognize.
var managerHandback = map[selfupdate.Manager]struct{ name, command string }{
	selfupdate.Homebrew: {"Homebrew", "brew upgrade trajector"},
	selfupdate.Scoop:    {"Scoop", "scoop update trajector"},
}

// upgradeLockWait is how long a manual upgrade waits for one already
// running on this machine — the background checker, or another
// terminal — before saying so. The one running takes minutes when it
// downloads, so waiting it out would look like a hang.
const upgradeLockWait = 2 * time.Second

// installation is this machine's trajector as selfupdate sees it.
func (m *Machine) installation() selfupdate.Installation {
	return selfupdate.Installation{
		ExecPath: m.deps.ExecPath,
		Version:  m.deps.Version,
		State:    m.deps.Layout.UpgradeState(),
		Lock:     m.deps.Layout.UpgradeLock(),
	}
}

// Upgrade replaces this binary with the newest published release and
// says which of the four things happened to it.
//
// Which release that is comes from the release source alone: the
// service's minimum client version decides whether a machine must
// upgrade, never what it upgrades to. Every exit that does not replace
// the binary leaves it exactly as it was — an upgrade that cannot
// happen must never cost the user a working trajector.
func (m *Machine) Upgrade(ctx context.Context, streams IO) error {
	out, err := selfupdate.Upgrade(ctx, m.installation(), m.deps.Releases, upgradeLockWait)
	if errors.Is(err, selfupdate.ErrBusy) {
		return errors.New("another upgrade is running on this machine; try again in a minute")
	}
	if unusable, ok := errors.AsType[*selfupdate.UnusableError](err); ok {
		fmt.Fprintf(streams.Out, "trajector %s does not start on this machine, so %s was kept.\n", unusable.Version, m.deps.Version)
		return fmt.Errorf("version %s was not installed: %s", unusable.Version, unusable.Reason)
	}
	if err != nil {
		return err
	}
	switch out.Kind {
	case selfupdate.Managed:
		hand := managerHandback[out.Manager]
		fmt.Fprintf(streams.Out, "This trajector was installed with %s, which owns the binary.\n", hand.name)
		fmt.Fprintf(streams.Out, "Run `%s` instead.\n", hand.command)
	case selfupdate.NotARelease:
		fmt.Fprintf(streams.Out, "This build reports version %s, which is not a published release.\n", out.From)
		fmt.Fprintln(streams.Out, "Nothing was changed.")
	case selfupdate.AlreadyNewest:
		fmt.Fprintf(streams.Out, "trajector %s is already the newest release.\n", out.From)
	case selfupdate.Upgraded:
		fmt.Fprintf(streams.Out, "Upgraded trajector %s -> %s.\n", out.From, out.To)
		// The proxy this build started keeps serving until something
		// replaces it; the takeover rule then prefers the newer release.
		fmt.Fprintln(streams.Out, "A proxy from the previous build may still be running; the next session replaces it.")
		m.sayPauseStands(streams)
	default:
		// Every outcome owes the user a sentence. An outcome no case
		// above answers is a gap in this switch, and saying nothing at
		// all would hide it behind a successful command.
		return fmt.Errorf("upgrade finished in a state this build cannot describe (%d)", out.Kind)
	}
	return nil
}

// sayPauseStands names what a pause that survived this upgrade still
// asks of the user. Which pauses those are, and what each of them then
// owes, is the pause reason's own answer: a newer build resumes nothing
// by itself. The pause is read here, never lifted. A routing table this
// build cannot read leaves the line out: the upgrade is already done,
// and a failure to add a hint to it must not report the upgrade as
// failed.
func (m *Machine) sayPauseStands(streams IO) {
	paused, err := m.routes.PausedReason()
	if err != nil {
		return
	}
	if next := paused.ExplainAfterUpgrade(); next != "" {
		fmt.Fprintln(streams.Out, next)
	}
}
