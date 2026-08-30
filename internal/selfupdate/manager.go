package selfupdate

import (
	"path/filepath"
	"strings"
)

// Manager is a package manager that can own an installation. A managed
// installation is upgraded through its manager rather than by
// overwriting the binary: the manager tracks a version and a receipt,
// and a file it did not put there is either reverted by the next
// command or left as an inconsistency the user has to unpick.
type Manager string

// The package managers this build can recognize as the owner of an
// installation.
const (
	Homebrew Manager = "homebrew"
	Scoop    Manager = "scoop"
)

// packageManager names the package manager that installed the binary
// at execPath, or the empty string when nothing recognizable did — a
// download, install.sh, or a build from source. Symbolic links are
// resolved first: both managers put the real binary inside their own
// tree and expose it through a link on the user's PATH, so the link's
// own location says nothing about who owns it.
func packageManager(execPath string) Manager {
	// A path that cannot be resolved — it was removed mid-run, or a
	// component is unreadable — is still worth classifying on its own
	// spelling, which for a manager's own tree is already conclusive.
	resolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		resolved = execPath
	}
	return managerForPath(resolved)
}

// managerForPath classifies a resolved path by the directories it runs
// through. Both separators are accepted whichever system this runs on,
// so a Windows path is classified the same way wherever it is read
// from.
func managerForPath(path string) Manager {
	segments := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	scoop := false
	for _, s := range segments {
		switch {
		// Homebrew keeps formula installations under Cellar and cask
		// ones under Caskroom, on macOS and on Linux alike.
		case s == "Cellar", s == "Caskroom":
			return Homebrew
		case strings.EqualFold(s, "scoop"):
			scoop = true
		// Scoop's own directory holds apps and the shims pointing into
		// them; a directory merely named "scoop" somewhere else does
		// not make an installation Scoop's.
		case scoop && (strings.EqualFold(s, "apps") || strings.EqualFold(s, "shims")):
			return Scoop
		}
	}
	return ""
}
