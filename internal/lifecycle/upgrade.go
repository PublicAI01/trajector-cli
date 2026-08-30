package lifecycle

import (
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// managerHandback is how to move an installation its package manager
// owns: the manager's name as the user knows it, and the command that
// does the upgrade. One entry per manager selfupdate can recognize.
var managerHandback = map[selfupdate.Manager]struct{ name, command string }{
	selfupdate.Homebrew: {"Homebrew", "brew upgrade trajector"},
	selfupdate.Scoop:    {"Scoop", "scoop update trajector"},
}

// Upgrade replaces this binary with the newest published release and
// says which of the four things happened to it.
//
// Which release that is comes from the release source alone: the
// service's minimum client version decides whether a machine must
// upgrade, never what it upgrades to. Every exit that does not replace
// the binary leaves it exactly as it was — an upgrade that cannot
// happen must never cost the user a working trajector.
func (m *Machine) Upgrade(io IO) error {
	out, err := selfupdate.Upgrade(m.deps.ExecPath, m.deps.Version, m.deps.Releases)
	if err != nil {
		return err
	}
	switch out.Kind {
	case selfupdate.Managed:
		hand := managerHandback[out.Manager]
		fmt.Fprintf(io.Out, "This trajector was installed with %s, which owns the binary.\n", hand.name)
		fmt.Fprintf(io.Out, "Run `%s` instead.\n", hand.command)
	case selfupdate.NotARelease:
		fmt.Fprintf(io.Out, "This build reports version %s, which is not a published release.\n", out.From)
		fmt.Fprintln(io.Out, "Nothing was changed.")
	case selfupdate.AlreadyNewest:
		fmt.Fprintf(io.Out, "trajector %s is already the newest release.\n", out.From)
	case selfupdate.Upgraded:
		fmt.Fprintf(io.Out, "Upgraded trajector %s -> %s.\n", out.From, out.To)
		// The proxy this build started keeps serving until something
		// replaces it; the takeover rule then prefers the newer release.
		fmt.Fprintln(io.Out, "A proxy from the previous build may still be running; the next session replaces it.")
	default:
		// Every outcome owes the user a sentence. An outcome no case
		// above answers is a gap in this switch, and saying nothing at
		// all would hide it behind a successful command.
		return fmt.Errorf("upgrade finished in a state this build cannot describe (%d)", out.Kind)
	}
	return nil
}
