package lifecycle_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
)

// newUpgradeEnv is a device on a published release, with an installed
// binary the machine may actually replace and a release source it
// upgrades from. The binary is a stand-in file rather than a build of
// trajector: what upgrade must get right is that the bytes on disk
// become the release's bytes, which a real binary would only make
// slower to assert.
func newUpgradeEnv(t *testing.T) (*env, *fakereleases.Server) {
	t.Helper()
	e := newEnv(t)
	e.deps.Version = "0.1.0"
	releases := fakereleases.New(t)
	e.deps.Releases = releases.IndexURL()
	e.installBinary("the 0.1.0 binary")
	return e, releases
}

// installBinary puts content at this device's executable path.
func (e *env) installBinary(content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.deps.ExecPath), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.deps.ExecPath, []byte(content), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

// installedBinary is what is on disk where this device's binary lives.
func (e *env) installedBinary() string {
	e.t.Helper()
	data, err := os.ReadFile(e.deps.ExecPath)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data)
}

// unreachableSource points this device at a release source that answers
// nothing, so a command that reports success has provably not consulted
// one.
func (e *env) unreachableSource() {
	e.deps.Releases = "http://releases.invalid/releases"
}

func TestUpgradeReplacesThisBinaryWithTheNewestRelease(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))

	if err := e.machine().Upgrade(e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if got := e.installedBinary(); got != "the 0.2.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
	out := e.stdout.String()
	if !strings.Contains(out, "Upgraded trajector 0.1.0 -> 0.2.0.") {
		t.Errorf("upgrade did not report the versions it moved between:\n%s", out)
	}
	// A proxy started by the old build is still serving the port; the
	// user has to know why the version they just installed is not the
	// one status reports.
	if !strings.Contains(out, "previous build may still be running") {
		t.Errorf("upgrade did not mention the running proxy:\n%s", out)
	}
}

func TestUpgradeOnTheNewestReleaseChangesNothing(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))

	if err := e.machine().Upgrade(e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if got := e.installedBinary(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
	if !strings.Contains(e.stdout.String(), "trajector 0.1.0 is already the newest release.") {
		t.Errorf("upgrade did not say the machine is current:\n%s", e.stdout)
	}
	if downloads := releases.Downloads(); len(downloads) != 0 {
		t.Errorf("upgrade downloaded %v with nothing to install", downloads)
	}
}

func TestUpgradeReportsAReleaseSourceThatIsRationingRequests(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	releases.Ration()

	err := e.machine().Upgrade(e.io())
	if err == nil {
		t.Fatal("Upgrade succeeded against a rationing release source")
	}
	if !strings.Contains(err.Error(), "try again later") {
		t.Errorf("error does not say the refusal is temporary: %v", err)
	}
	if got := e.installedBinary(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
}

func TestUpgradeOfAnInstallationAPackageManagerOwns(t *testing.T) {
	for _, c := range []struct {
		name    string
		tree    []string
		manager string
		command string
	}{
		{"homebrew", []string{"Cellar", "trajector", "0.1.0", "bin"}, "Homebrew", "brew upgrade trajector"},
		{"scoop", []string{"scoop", "apps", "trajector", "current"}, "Scoop", "scoop update trajector"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _ := newUpgradeEnv(t)
			e.deps.ExecPath = filepath.Join(append([]string{t.TempDir()}, append(c.tree, "trajector")...)...)
			e.installBinary("the binary the manager installed")
			e.unreachableSource()

			// Overwriting a managed binary would be undone by the
			// manager's next command, or leave a version its records
			// disagree with.
			if err := e.machine().Upgrade(e.io()); err != nil {
				t.Fatalf("Upgrade: %v", err)
			}

			out := e.stdout.String()
			if !strings.Contains(out, c.manager) || !strings.Contains(out, c.command) {
				t.Errorf("upgrade did not hand the installation back to its manager:\n%s", out)
			}
			if got := e.installedBinary(); got != "the binary the manager installed" {
				t.Errorf("installed binary is %q", got)
			}
		})
	}
}

func TestUpgradeOfABuildThatIsNotAPublishedRelease(t *testing.T) {
	e, _ := newUpgradeEnv(t)
	e.deps.Version = "dev"
	e.unreachableSource()

	// A build from a checkout has no place in the version order, and
	// replacing it with a release would discard whatever it was built
	// to test.
	if err := e.machine().Upgrade(e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	out := e.stdout.String()
	if !strings.Contains(out, "not a published release") || !strings.Contains(out, "Nothing was changed.") {
		t.Errorf("upgrade did not explain what it did with a development build:\n%s", out)
	}
	if got := e.installedBinary(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
}
