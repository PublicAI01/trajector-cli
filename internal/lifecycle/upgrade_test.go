package lifecycle_test

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// newUpgradeEnv is a device on a published release, with an installed
// binary the machine may actually replace and a release source it
// upgrades from. The installed binary is a stand-in file: nothing runs
// it. A release that is to be installed has to be a real build
// (fakereleases.BuildRelease), because the upgrade runs it once before
// putting it in place.
func newUpgradeEnv(t *testing.T) (*env, *fakereleases.Server) {
	t.Helper()
	e := newEnv(t)
	e.deps.Version = "0.1.0"
	releases := fakereleases.New(t)
	e.deps.Releases = releases.IndexURL()
	e.installBinary("the 0.1.0 binary")
	e.environmentNamesThisDevice()
	return e, releases
}

// environmentNamesThisDevice points this test process's environment at
// the device's directories. A release an upgrade installs checks
// itself and finishes its install as a process of its own, which finds
// the device's directories from the environment it inherits, as every
// trajector command does; left alone it would find the developer's.
// The sandbox layout puts every directory under one root, which the
// three variables then name.
func (e *env) environmentNamesThisDevice() {
	e.t.Helper()
	root := filepath.Dir(filepath.Dir(e.layout().UpgradeState()))
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		e.t.Setenv(name, root)
	}
	if got, err := userdirs.Resolve(userdirs.Host()); err != nil || got != e.layout() {
		e.t.Fatalf("the environment names %+v (%v), not this device's %+v", got, err, e.layout())
	}
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

// publishNewer publishes 0.2.0 as a real build, the only kind of
// release an upgrade installs, and returns its bytes.
func publishNewer(t *testing.T, releases *fakereleases.Server) string {
	t.Helper()
	binary := fakereleases.BuildRelease(t, "0.2.0")
	releases.Publish(t, "0.2.0", binary)
	return string(binary)
}

// unreachableSource points this device at a release source that answers
// nothing, so a command that reports success has provably not consulted
// one.
func (e *env) unreachableSource() {
	e.deps.Releases = "http://releases.invalid/releases"
}

func TestUpgradeReplacesThisBinaryWithTheNewestRelease(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	release := publishNewer(t, releases)

	if err := e.machine().Upgrade(t.Context(), e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if e.installedBinary() != release {
		t.Error("the installed binary is not the 0.2.0 release")
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

	if err := e.machine().Upgrade(t.Context(), e.io()); err != nil {
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

	err := e.machine().Upgrade(t.Context(), e.io())
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
			if err := e.machine().Upgrade(t.Context(), e.io()); err != nil {
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
	if err := e.machine().Upgrade(t.Context(), e.io()); err != nil {
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

func TestUpgradeUnderARedactionDriftPauseNamesTheSecondStep(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	publishNewer(t, releases)
	e.sandbox.PauseByBuild(proxytest.PauseRedactionDrift, e.deps.Version)

	if err := e.machine().Upgrade(t.Context(), e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	out := e.stdout.String()
	if !strings.Contains(out, "Upgraded trajector 0.1.0 -> 0.2.0.") {
		t.Fatalf("upgrade did not replace the binary:\n%s", out)
	}
	if !strings.Contains(out, proxytest.PauseRedactionDrift.ExplainAfterUpgrade()) {
		t.Errorf("upgrade did not name the step that resumes recording:\n%s", out)
	}
	// The new binary is not evidence that it reads the session files;
	// only doctor reads them, so the pause stands until it runs.
	if got := e.sandbox.PausedReason(); got != proxytest.PauseRedactionDrift {
		t.Errorf("PausedReason = %q, want the pause left standing", got)
	}
}

func TestUpgradeWithRecordingNotPausedNamesNoFurtherStep(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	publishNewer(t, releases)

	if err := e.machine().Upgrade(t.Context(), e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if out := e.stdout.String(); strings.Contains(out, proxytest.PauseRedactionDrift.ExplainAfterUpgrade()) {
		t.Errorf("upgrade sent a recording device to doctor:\n%s", out)
	}
}

func TestUpgradeThatInstallsNothingNamesNoFurtherStep(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
	e.sandbox.PauseByBuild(proxytest.PauseRedactionDrift, e.deps.Version)

	if err := e.machine().Upgrade(t.Context(), e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	// Nothing about this build changed, so doctor would reach the
	// judgement that paused recording in the first place.
	if out := e.stdout.String(); strings.Contains(out, proxytest.PauseRedactionDrift.ExplainAfterUpgrade()) {
		t.Errorf("a machine already on the newest release was sent to doctor:\n%s", out)
	}
}

func commandTheUpgradeHintNames(t *testing.T, out string) string {
	t.Helper()
	_, hint, ok := strings.Cut(out, "Recording is paused")
	if !ok {
		t.Fatalf("upgrade named no step for a paused device:\n%s", out)
	}
	_, quoted, ok := strings.Cut(hint, "`")
	if !ok {
		t.Fatalf("the step upgrade named carries no command:\n%s", out)
	}
	cmd, _, _ := strings.Cut(quoted, "`")
	return cmd
}

func TestUpgradeAndStatusNameOneRemedyForARedactionDriftPause(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	publishNewer(t, releases)
	e.sandbox.PauseByBuild(proxytest.PauseRedactionDrift, e.deps.Version)

	if err := e.machine().Upgrade(t.Context(), e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	step := commandTheUpgradeHintNames(t, e.stdout.String())
	e.stdout.Reset()

	// status puts the command on the fix line, where it stands alone
	// so it can be copied; the backticks upgrade writes it in belong to
	// a sentence, not to a line of its own.
	if out := e.statusOutput(); !strings.Contains(out, "fix:  ") || !strings.Contains(out, step) {
		t.Errorf("upgrade sends the user to %q; status explains the pause as %q", step, out)
	}
}

func TestUpgradeToAReleaseThatDoesNotStartHereKeepsThisOne(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.2.0", []byte("these bytes are not a program"))

	err := e.machine().Upgrade(t.Context(), e.io())
	if err == nil || !strings.Contains(err.Error(), "version 0.2.0 was not installed: it cannot be run") {
		t.Fatalf("Upgrade = %v, want the release named with what running it did", err)
	}
	if got := e.installedBinary(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
	if out := e.stdout.String(); !strings.Contains(out, "trajector 0.2.0 does not start on this machine, so 0.1.0 was kept.\n") {
		t.Errorf("upgrade did not say which version was kept:\n%s", out)
	}
}

func TestUpgradeWhileAnotherUpgradeRunsSaysSo(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	publishNewer(t, releases)
	lock := e.layout().UpgradeLock()
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := fsatomic.Lock(lock, time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	start := time.Now()
	err = e.machine().Upgrade(t.Context(), e.io())
	if err == nil || err.Error() != "another upgrade is running on this machine; try again in a minute" {
		t.Fatalf("Upgrade = %v, want the other upgrade named", err)
	}
	// A manual upgrade gives the other one a moment rather than none,
	// and not the minutes a download can take.
	if waited := time.Since(start); waited < 2*time.Second || waited > 10*time.Second {
		t.Errorf("upgrade waited %s for the other one, want about 2s", waited)
	}
	if got := e.installedBinary(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
}

// stateEntries is every name in the directory the upgrade state file
// is in.
func (e *env) stateEntries() []string {
	e.t.Helper()
	entries, err := os.ReadDir(filepath.Dir(e.layout().UpgradeState()))
	if err != nil && !os.IsNotExist(err) {
		e.t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestVerifyUpgradeServesOnItsOwnAndLeavesThisDeviceAsItWas(t *testing.T) {
	e := newEnv(t)
	before := e.stateEntries()

	if err := e.machine().VerifyUpgrade(t.Context()); err != nil {
		t.Fatalf("VerifyUpgrade: %v", err)
	}

	// The proxy it started was on a port and in a directory of its own:
	// the directory is gone, nothing listens where this device's
	// sessions reach their proxy, and the paired device's token, which
	// that proxy never looked for, sent nothing to the service.
	if after := e.stateEntries(); !slices.Equal(after, before) {
		t.Errorf("the state directory holds %v after the check, %v before", after, before)
	}
	if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", e.deps.ProxyAddr); err == nil {
		conn.Close()
		t.Errorf("something listens at %s, this device's proxy address", e.deps.ProxyAddr)
	}
	if got := e.service.Requests(); len(got) != 0 {
		t.Errorf("the check sent %d requests to the service", len(got))
	}
}

func TestVerifyUpgradeThatCannotMakeItsDirectorySaysSo(t *testing.T) {
	e := newEnv(t)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.deps.Layout = proxytest.SandboxLayout(t, blocked)

	err := e.machine().VerifyUpgrade(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "creating a directory to start the proxy in: ") {
		t.Errorf("VerifyUpgrade = %v, want the directory it could not make", err)
	}
}

func TestFinishUpgradeHasNothingToDoAndRunsAgainAlike(t *testing.T) {
	e := newEnv(t)
	before := e.stateEntries()
	for range 2 {
		if err := e.machine().FinishUpgrade(t.Context()); err != nil {
			t.Fatalf("FinishUpgrade: %v", err)
		}
	}
	if after := e.stateEntries(); !slices.Equal(after, before) {
		t.Errorf("the state directory holds %v after finishing, %v before", after, before)
	}
}

func TestUpgradeToAReleaseWhoseProxyDoesNotStartHereKeepsThisOne(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	publishNewer(t, releases)
	// The 0.2.0 build runs, but the state directory it starts its proxy
	// under is, as that build finds it, a file.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", blocked)

	err := e.machine().Upgrade(t.Context(), e.io())
	if err == nil || !strings.HasPrefix(err.Error(), "version 0.2.0 was not installed: its proxy does not start here: ") {
		t.Fatalf("Upgrade = %v, want the release named with what its proxy did", err)
	}
	if got := e.installedBinary(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q, want the previous one put back", got)
	}
	if out := e.stdout.String(); out != "trajector 0.2.0 does not start on this machine, so 0.1.0 was kept.\n" {
		t.Errorf("upgrade printed %q, want only which version was kept", out)
	}
}
