package lifecycle_test

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestUpgradeNeverMovesBackToAnOlderRelease(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	// A release withdrawn after this machine installed it, or a machine
	// running a build from a tag ahead of the source: either way the
	// newest thing published is behind, and behind is not an upgrade.
	releases.Publish(t, "0.0.9", []byte("an older binary"))

	if err := e.machine().Upgrade(e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if got := e.installedBinary(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
	if !strings.Contains(e.stdout.String(), "already the newest release") {
		t.Errorf("upgrade did not say the machine is current:\n%s", e.stdout)
	}
}

func TestUpgradeMovesToAPrereleaseBecauseThatIsWhatIsPublished(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	// Every 0.x release is published as a pre-release. Skipping them
	// would leave every beta machine reporting itself current forever.
	releases.Publish(t, "0.2.0-rc.1", []byte("the 0.2.0-rc.1 binary"))

	if err := e.machine().Upgrade(e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if got := e.installedBinary(); got != "the 0.2.0-rc.1 binary" {
		t.Errorf("installed binary is %q", got)
	}
}

func TestUpgradeLeavesTheBinaryUntouchedWhenTheDownloadFailsVerification(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	releases.Corrupt(t, "0.2.0", runtime.GOOS, runtime.GOARCH)
	before, err := os.ReadFile(e.deps.ExecPath)
	if err != nil {
		t.Fatal(err)
	}

	err = e.machine().Upgrade(e.io())
	if err == nil {
		t.Fatal("Upgrade installed a download that failed verification")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error does not name the mismatch: %v", err)
	}

	after, readErr := os.ReadFile(e.deps.ExecPath)
	if readErr != nil {
		t.Fatalf("the binary is gone after a failed upgrade: %v", readErr)
	}
	if string(after) != string(before) {
		t.Errorf("the binary changed after a failed upgrade: %q", after)
	}
	// A failed upgrade must not leave a staged binary next to the real
	// one either.
	entries, err := os.ReadDir(filepath.Dir(e.deps.ExecPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("a failed upgrade left %d files in the install directory", len(entries))
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

func TestUpgradeOfAnInstallationHomebrewOwns(t *testing.T) {
	e, _ := newUpgradeEnv(t)
	e.deps.ExecPath = filepath.Join(t.TempDir(), "Cellar", "trajector", "0.1.0", "bin", "trajector")
	e.installBinary("the binary homebrew installed")
	e.unreachableSource()

	// Overwriting a managed binary would be undone by the manager's next
	// command, or leave a version its records disagree with.
	if err := e.machine().Upgrade(e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	out := e.stdout.String()
	if !strings.Contains(out, "Homebrew") || !strings.Contains(out, "brew upgrade trajector") {
		t.Errorf("upgrade did not hand the installation back to its manager:\n%s", out)
	}
	if got := e.installedBinary(); got != "the binary homebrew installed" {
		t.Errorf("installed binary is %q", got)
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

func TestUpgradeSweepsWhatAnInterruptedUpgradeLeftBehind(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
	dir := filepath.Dir(e.deps.ExecPath)
	residue := filepath.Join(dir, filepath.Base(e.deps.ExecPath)+".old-9f2c")
	if err := os.WriteFile(residue, []byte("a previous binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Even a run that installs nothing tidies up: on Windows the file
	// an earlier upgrade stepped aside could not be deleted while it
	// was still the running image.
	if err := e.machine().Upgrade(e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Errorf("residue of an earlier upgrade is still there (%v)", err)
	}
}

func TestUpgradeSaysSoWhenTheSourceHasPublishedNothing(t *testing.T) {
	e, _ := newUpgradeEnv(t)

	err := e.machine().Upgrade(e.io())
	if err == nil {
		t.Fatal("Upgrade succeeded against a source that has published nothing")
	}
	if got := e.installedBinary(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
}
