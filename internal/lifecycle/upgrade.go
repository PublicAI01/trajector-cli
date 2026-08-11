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

// versionStanding is where this build sits against the minimum client
// version the service last announced.
type versionStanding int

const (
	// versionSatisfied: the service named a minimum and this build is at
	// or above it — or it named none at all.
	versionSatisfied versionStanding = iota
	// versionBehind: this build is older than the service's minimum.
	versionBehind
	// versionUnknown: no order exists between the two, so neither of the
	// above can be claimed.
	versionUnknown
)

// standing orders this build against the service's stated minimum.
//
// The service sends min_client_version on every acknowledgement, not
// only when it matters, because it assumes the client works out whether
// it matters. Nothing did: status and doctor relayed the minimum
// verbatim, so one successful upload left a satisfied build being told
// to upgrade for good. That is not a harmful instruction — running
// upgrade would report there is nothing to install — but it costs the
// sentence its meaning. When the service really does refuse this build,
// status prints the same two lines it has been printing all along, and
// a user who learned to skip them skips them exactly then.
//
// So the judgement lives here. An unorderable pair — a dev build, or a
// minimum that is not a semantic version — is versionUnknown rather
// than versionBehind: not knowing is not the same as being behind, and
// a refusal announces itself on its own path (the 426 on upload) where
// the service's word settles it without any comparison.
func standing(minVersion, version string) versionStanding {
	if minVersion == "" {
		return versionSatisfied
	}
	order, ok := proxylife.Compare(version, minVersion)
	switch {
	case !ok:
		return versionUnknown
	case order < 0:
		return versionBehind
	default:
		return versionSatisfied
	}
}

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
