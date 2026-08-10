package lifecycle

import (
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// upgradeHint is the sentence every surface that has found this build
// behind prints. One spelling, so a user who reads it in three places
// is being told to do one thing.
const upgradeHint = "Run `trajector upgrade` to install the newest release."

// managerUpgrade is how to move an installation its package manager
// owns. Overwriting the binary under a manager would work once and
// then be undone, or left as a version the manager's records disagree
// with, so these installations are handed back to their manager.
var managerUpgrade = map[string]struct{ name, command string }{
	selfupdate.Homebrew: {"Homebrew", "brew upgrade trajector"},
	selfupdate.Scoop:    {"Scoop", "scoop update trajector"},
}

// Upgrade replaces this binary with the newest published release.
//
// Which release that is comes from the release source alone: the
// service's minimum client version decides whether a machine must
// upgrade, never what it upgrades to. The archive is checked against
// the release's published checksum before anything is replaced, and
// every exit that does not replace the binary leaves it exactly as it
// was — an upgrade that cannot happen must never cost the user a
// working trajector.
func (m *Machine) Upgrade(io IO) error {
	// Whatever an interrupted earlier upgrade left beside the binary
	// goes now, whether or not this run installs anything.
	selfupdate.SweepResidue(m.deps.ExecPath)

	if manager, ok := managerUpgrade[selfupdate.PackageManager(m.deps.ExecPath)]; ok {
		fmt.Fprintf(io.Out, "This trajector was installed with %s, which owns the binary.\n", manager.name)
		fmt.Fprintf(io.Out, "Run `%s` instead.\n", manager.command)
		return nil
	}

	current := m.deps.Version
	if _, comparable := proxylife.Compare(current, current); !comparable {
		// A build from a checkout announces something no release ever
		// will. Replacing it with a release would silently discard
		// whatever it was built to test.
		fmt.Fprintf(io.Out, "This build reports version %s, which is not a published release.\n", current)
		fmt.Fprintln(io.Out, "Nothing was changed.")
		return nil
	}

	source := selfupdate.New(m.deps.Releases, current)
	goos, goarch := selfupdate.HostPlatform()
	release, err := source.Newest(goos, goarch)
	if err != nil {
		return err
	}
	if order, ok := proxylife.Compare(release.Version, current); ok && order <= 0 {
		fmt.Fprintf(io.Out, "trajector %s is already the newest release.\n", current)
		return nil
	}

	fmt.Fprintf(io.Out, "Downloading trajector %s...\n", release.Version)
	binary, err := source.Download(release, goos)
	if err != nil {
		return err
	}
	if err := selfupdate.Install(m.deps.ExecPath, binary); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Upgraded trajector %s -> %s.\n", current, release.Version)
	// The proxy this build started keeps serving until something
	// replaces it; the takeover rule then prefers the newer release.
	fmt.Fprintln(io.Out, "A proxy from the previous build may still be running; the next session replaces it.")
	return nil
}
