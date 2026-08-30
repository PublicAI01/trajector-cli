// Package selfupdate moves this installation to a newer published
// release. One call answers the whole question: whether a package
// manager owns the binary, whether this build has a place in the
// release order at all, which release is the newest published one, and
// — when there is one to move to — fetching it, checking it against
// its published checksum, and swapping it in. It decides nothing the
// user reads: the Outcome carries the facts and the caller writes every
// sentence. A binary it has not verified is never installed, and every
// exit that installs nothing leaves the previous binary in place and
// runnable.
package selfupdate

import "github.com/PublicAI01/trajector-cli/internal/semver"

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

// Upgrade moves the installation at execPath, announcing version, to
// the newest release published at indexURL.
//
// Which release that is comes from the release index alone: nothing
// else — no service, no argument — names what a machine upgrades to.
// The archive is checked against the release's published checksum
// before anything is replaced, so a run that returns an error has left
// the machine exactly as it found it, minus whatever residue an
// interrupted earlier upgrade had left beside the binary.
func Upgrade(execPath, version, indexURL string) (Outcome, error) {
	// Housekeeping comes first, whether or not this run installs
	// anything: on Windows the binary an earlier upgrade stepped aside
	// could not be deleted while it was still the running image.
	SweepResidue(execPath)

	if manager := packageManager(execPath); manager != "" {
		return Outcome{Kind: Managed, Manager: manager, From: version}, nil
	}
	if !semver.Comparable(version) {
		return Outcome{Kind: NotARelease, From: version}, nil
	}

	goos, goarch := hostPlatform()
	src := newSource(indexURL, version)
	rel, err := src.newest(goos, goarch)
	if err != nil {
		return Outcome{}, err
	}
	// A release that is not strictly newer is not an upgrade: a
	// withdrawn release, or a machine built from a tag ahead of the
	// source, must not be moved backwards.
	if order, ok := semver.Compare(rel.version, version); !ok || order <= 0 {
		return Outcome{Kind: AlreadyNewest, From: version}, nil
	}

	binary, err := src.download(rel, goos)
	if err != nil {
		return Outcome{}, err
	}
	if err := install(execPath, binary); err != nil {
		return Outcome{}, err
	}
	return Outcome{Kind: Upgraded, From: version, To: rel.version}, nil
}
