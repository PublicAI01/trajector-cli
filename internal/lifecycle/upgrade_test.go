package lifecycle_test

import (
	"cmp"
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
	"github.com/PublicAI01/trajector-cli/internal/userconfig"
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

// checksOnItsOwn is the line a manual upgrade ends with when the build
// it leaves in place also checks for newer releases by itself.
const checksOnItsOwn = `trajector also checks for a newer release once a day on its own; set "auto_upgrade": false in config.json to stop that.`

func TestUpgradeSaysTheDeviceAlsoChecksOnItsOwnOnlyWhenItDoes(t *testing.T) {
	cases := map[string]struct {
		arrange   func(e *env, releases *fakereleases.Server)
		release   bool
		auto      bool
		agreement string
		says      bool
	}{
		"already the newest release": {
			arrange: func(_ *env, releases *fakereleases.Server) {
				releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
			},
			release: true, auto: true, says: true,
		},
		"already the newest release, on an agreement accepted before the check was in it": {
			arrange: func(_ *env, releases *fakereleases.Server) {
				releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
			},
			release: true, auto: true, agreement: agreementBeforeTheCheck(t),
		},
		"already the newest release, auto_upgrade false": {
			arrange: func(_ *env, releases *fakereleases.Server) {
				releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
			},
			release: true,
		},
		"already the newest release, built from source": {
			arrange: func(_ *env, releases *fakereleases.Server) {
				releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
			},
			auto: true,
		},
		"upgraded": {
			arrange: func(_ *env, releases *fakereleases.Server) { publishNewer(t, releases) },
			release: true, auto: true, says: true,
		},
		// What is in place afterwards is the published release.
		"upgraded from a build made from source": {
			arrange: func(_ *env, releases *fakereleases.Server) { publishNewer(t, releases) },
			auto:    true, says: true,
		},
		// This build's agreement is the one the device has not
		// accepted; the build installed asks no less of it.
		"upgraded, on an agreement accepted before the check was in it": {
			arrange: func(_ *env, releases *fakereleases.Server) { publishNewer(t, releases) },
			release: true, auto: true, agreement: agreementBeforeTheCheck(t),
		},
		"upgraded, auto_upgrade false": {
			arrange: func(_ *env, releases *fakereleases.Server) { publishNewer(t, releases) },
			release: true,
		},
		"owned by a package manager": {
			arrange: func(e *env, _ *fakereleases.Server) {
				e.deps.ExecPath = exeName(filepath.Join(e.deps.Home, "Cellar", "trajector", "0.1.0", "bin", "trajector"))
				e.installBinary("the binary Homebrew installed")
				e.unreachableSource()
			},
			release: true, auto: true,
		},
		"not a published release": {
			arrange: func(e *env, _ *fakereleases.Server) {
				e.deps.Version = "dev"
				e.unreachableSource()
			},
			release: true, auto: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e, releases := newUpgradeEnv(t)
			e.deps.ReleaseBuild, e.deps.AutoUpgrade = tc.release, tc.auto
			e.sandbox.AcceptAgreement(cmp.Or(tc.agreement, proxytest.AgreementVersion), "2026-08-01T00:00:00Z")
			tc.arrange(e, releases)

			if err := e.machine().Upgrade(t.Context(), e.io()); err != nil {
				t.Fatalf("Upgrade: %v", err)
			}
			if got := strings.Contains(e.stdout.String(), checksOnItsOwn+"\n"); got != tc.says {
				t.Errorf("said the device checks on its own = %t, want %t; stdout:\n%s", got, tc.says, e.stdout)
			}
			if strings.Contains(e.stderr.String(), "checks for a newer release") {
				t.Errorf("stderr = %q, want the line on stdout only", e.stderr)
			}
		})
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
			e.deps.ExecPath = exeName(filepath.Join(append([]string{t.TempDir()}, append(c.tree, "trajector")...)...))
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

// notices is what this device has waiting for the user's next command,
// taken the way that command takes it.
func (e *env) notices() []string {
	e.t.Helper()
	return e.machine().TakeNotices()
}

const upgradedTestv = "trajector upgraded itself to testv in the background."

func TestFinishUpgradeTellsTheNextCommandOnceThatThisBuildIsInstalled(t *testing.T) {
	e := newEnv(t)
	for range 2 {
		if err := e.machine().FinishUpgrade(t.Context()); err != nil {
			t.Fatalf("FinishUpgrade: %v", err)
		}
	}

	// Run twice, it still leaves the line once.
	if got := e.notices(); !slices.Equal(got, []string{upgradedTestv}) {
		t.Errorf("notices = %q, want only %q", got, upgradedTestv)
	}
	if got := e.notices(); len(got) != 0 {
		t.Errorf("notices = %q after they were taken, want none", got)
	}
}

func TestFinishUpgradeUnderAnAgreementThisDeviceHasNotAcceptedSaysTheAgreementChanged(t *testing.T) {
	e := newEnv(t)
	e.sandbox.AcceptAgreement("2026-01-01", "2026-01-01T00:00:00Z")

	if err := e.machine().FinishUpgrade(t.Context()); err != nil {
		t.Fatalf("FinishUpgrade: %v", err)
	}

	want := []string{upgradedTestv, "trajector: " + proxytest.PauseConsentReconfirm.Explain()}
	if got := e.notices(); !slices.Equal(got, want) {
		t.Errorf("notices = %q, want %q", got, want)
	}
	// Recording is paused by the next session, which every build does
	// on finding the agreement changed; finishing an install only says
	// so.
	if got := e.sandbox.PausedReason(); got != "" {
		t.Errorf("PausedReason = %q, want no pause set by finishing", got)
	}
}

func TestFinishUpgradeUnderTheCurrentAgreementOrNoneSaysNothingOfIt(t *testing.T) {
	for name, accepted := range map[string]string{"current": proxytest.AgreementVersion, "none": ""} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if accepted != "" {
				e.sandbox.AcceptAgreement(accepted, "2026-08-01T00:00:00Z")
			}
			if err := e.machine().FinishUpgrade(t.Context()); err != nil {
				t.Fatalf("FinishUpgrade: %v", err)
			}
			if got := e.notices(); !slices.Equal(got, []string{upgradedTestv}) {
				t.Errorf("notices = %q, want only %q", got, upgradedTestv)
			}
		})
	}
}

func TestFinishUpgradeUnderARedactionDriftPauseNamesTheStepLeft(t *testing.T) {
	e := newEnv(t)
	e.sandbox.PauseByBuild(proxytest.PauseRedactionDrift, "0.0.9")

	if err := e.machine().FinishUpgrade(t.Context()); err != nil {
		t.Fatalf("FinishUpgrade: %v", err)
	}

	want := []string{upgradedTestv, proxytest.PauseRedactionDrift.ExplainAfterUpgrade()}
	if got := e.notices(); !slices.Equal(got, want) {
		t.Errorf("notices = %q, want %q", got, want)
	}
}

func TestFinishUpgradeThatCannotLeaveItsLinesFails(t *testing.T) {
	e := newEnv(t)
	e.obstruct(filepath.Dir(e.layout().Notices()))

	// The older build keeps the install unfinished, and it is run again.
	if err := e.machine().FinishUpgrade(t.Context()); err == nil {
		t.Error("FinishUpgrade succeeded without leaving its lines")
	}
}

func TestUpgradeSaysWhatTheInstalledBuildLeftWithoutRepeatingItself(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	publishNewer(t, releases)
	e.sandbox.AcceptAgreement("2026-01-01", "2026-01-01T00:00:00Z")
	e.sandbox.PauseByBuild(proxytest.PauseRedactionDrift, e.deps.Version)

	if err := e.machine().Upgrade(t.Context(), e.io()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	out, errOut := e.stdout.String(), e.stderr.String()
	// The agreement line is the installed build's alone to say.
	if !strings.Contains(errOut, "trajector: "+proxytest.PauseConsentReconfirm.Explain()+"\n") {
		t.Errorf("stderr = %q, want the changed agreement named", errOut)
	}
	// This command already said it upgraded, in the foreground, and
	// which step the pause still asks for.
	if strings.Contains(errOut, "in the background") {
		t.Errorf("stderr = %q, want no background upgrade announced by the command that ran it", errOut)
	}
	next := proxytest.PauseRedactionDrift.ExplainAfterUpgrade()
	if n := strings.Count(out+errOut, next); n != 1 {
		t.Errorf("the remaining step is said %d times, want once:\n%s\n%s", n, out, errOut)
	}
	if got := e.notices(); len(got) != 0 {
		t.Errorf("notices = %q after the upgrade said them, want none", got)
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

// writeUpgradeState puts content where this device's upgrade state file
// lives, as the background check would have left it.
func (e *env) writeUpgradeState(content string) {
	e.t.Helper()
	path := e.layout().UpgradeState()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// readUserConfig hands the machine what the user config says about
// automatic upgrades, as the command line does when it opens one.
func (e *env) readUserConfig() {
	e.t.Helper()
	cfg, _ := userconfig.Read(e.layout().ConfigFile())
	e.deps.AutoUpgrade = cfg.UpgradesAutomatically()
}

func TestStatusSaysWhatTheLastUpgradeCheckFound(t *testing.T) {
	cases := map[string]struct {
		execPath  func(e *env) string
		config    string
		agreement string
		state     string
		want      string
	}{
		"up to date": {
			state: `{"checked_at": "2026-10-12T08:30:00Z", "result": "newest"}`,
			want:  "Automatic upgrades: last checked 2026-10-12T08:30:00Z, up to date.",
		},
		"failed": {
			state: `{"checked_at": "2026-10-12T08:30:00Z", "result": "failed", "error": "fetching the release index: 503 Service Unavailable"}`,
			want:  "Automatic upgrades: the last attempt at 2026-10-12T08:30:00Z failed: fetching the release index: 503 Service Unavailable. Run `trajector upgrade`.",
		},
		"turned off in the config file": {
			config: `{"auto_upgrade": false}`,
			state:  `{"checked_at": "2026-10-12T08:30:00Z", "result": "newest"}`,
			want:   "Automatic upgrades: off (config.json).",
		},
		"config file unreadable": {
			config: "{not json",
			state:  `{"checked_at": "2026-10-12T08:30:00Z", "result": "newest"}`,
			want:   "Automatic upgrades: off (config.json could not be read).",
		},
		"never checked": {
			want: "Automatic upgrades: on; no check has run yet.",
		},
		"managed by Homebrew": {
			execPath: func(e *env) string {
				return exeName(filepath.Join(e.deps.Home, "Cellar", "trajector", "0.3.7", "bin", "trajector"))
			},
			state: `{"checked_at": "2026-10-12T08:30:00Z", "result": "managed"}`,
			want:  "Automatic upgrades: last checked 2026-10-12T08:30:00Z, managed by Homebrew.",
		},
		"upgraded": {
			state: `{"checked_at": "2026-10-12T08:30:00Z", "result": "upgraded", "installed": "0.4.0"}`,
			want:  "Automatic upgrades: last checked 2026-10-12T08:30:00Z, upgraded to 0.4.0.",
		},
		"a state file that does not parse": {
			state: `{cut`,
			want:  "Automatic upgrades: on; what the last check found could not be read: selfupdate: reading ",
		},
		"not a release build": {
			state: `{"checked_at": "2026-10-12T08:30:00Z", "result": "newest"}`,
			want:  "Automatic upgrades: off (not a release build).",
		},
		"an agreement accepted before the check was in it": {
			agreement: agreementBeforeTheCheck(t),
			state:     `{"checked_at": "2026-10-12T08:30:00Z", "result": "failed", "error": "fetching the release index: 503 Service Unavailable"}`,
			want:      "Automatic upgrades: off until you accept the current data agreement; run `trajector enable` (with --no-proxy and --no-earlier if you use them).",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.deps.ReleaseBuild = name != "not a release build"
			e.sandbox.AcceptAgreement(cmp.Or(tc.agreement, proxytest.AgreementVersion), "2026-08-01T00:00:00Z")
			if tc.execPath != nil {
				e.deps.ExecPath = tc.execPath(e)
			}
			if tc.config != "" {
				e.sandbox.WriteUserConfig(tc.config)
			}
			e.readUserConfig()
			if tc.state != "" {
				e.writeUpgradeState(tc.state)
			}
			if _, err := e.machine().Status(t.Context(), e.project, e.io()); err != nil {
				t.Fatalf("Status: %v", err)
			}
			if out := e.stdout.String(); !strings.Contains(out, tc.want) {
				t.Errorf("status = %s\nwant it to contain %q", out, tc.want)
			}
		})
	}
}
