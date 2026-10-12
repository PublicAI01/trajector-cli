package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
)

func TestUpgradeTakesNoArguments(t *testing.T) {
	e := clitest.New(t)

	got := e.Run("upgrade", "0.2.0")
	if got.Exit != 2 {
		t.Fatalf("exit = %d, want 2 (stdout: %q)", got.Exit, got.Stdout)
	}
	// Which release to move to is not the user's to name: the release
	// source decides, so an argument is a misunderstanding to correct
	// rather than a version to honor.
	if !strings.Contains(got.Stderr, "usage: trajector upgrade") {
		t.Errorf("stderr = %q, want the usage line", got.Stderr)
	}
}

func TestUpgradeOfABuildFromACheckoutInstallsNothing(t *testing.T) {
	e := clitest.New(t)
	releases := fakereleases.New(t)
	releases.Publish(t, "9.9.9", []byte("a much newer binary"))
	e.SetReleasesURL(releases.IndexURL())

	// A test binary announces the development version, which no
	// release order contains.
	got := e.Run("upgrade")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "not a published release") {
		t.Errorf("stdout = %q, want the development build explained", got.Stdout)
	}
	if downloads := releases.Downloads(); len(downloads) != 0 {
		t.Errorf("upgrade downloaded %v from a build it will not replace", downloads)
	}
}

func TestUpgradeOfABuildFromACheckoutNamesNoStepForAPausedDevice(t *testing.T) {
	e := clitest.New(t)
	releases := fakereleases.New(t)
	releases.Publish(t, "9.9.9", []byte("a much newer binary"))
	e.SetReleasesURL(releases.IndexURL())
	e.Sandbox().PauseByBuild(proxytest.PauseRedactionDrift, "9.9.8")

	got := e.Run("upgrade")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	// The step that resumes recording follows a build that changed. This
	// one did not, so doctor would reach the judgement that paused
	// recording in the first place.
	if strings.Contains(got.Stdout, proxytest.PauseRedactionDrift.ExplainAfterUpgrade()) {
		t.Errorf("stdout = %q, want no step that changes nothing", got.Stdout)
	}
	if got := e.Sandbox().PausedReason(); got != proxytest.PauseRedactionDrift {
		t.Errorf("PausedReason = %q, want the pause left standing", got)
	}
}

func TestUpgradeFailsLoudlyOnAConfigFileItCannotRead(t *testing.T) {
	e := clitest.New(t)
	e.WriteConfig("{not json")

	// The release source is configured in the same file as the service
	// endpoint, and read the same way: a file this command cannot read
	// might be the one naming a source other than the default.
	got := e.Run("upgrade")
	if got.Exit != 1 || !strings.Contains(got.Stderr, "config.json") {
		t.Errorf("upgrade = exit %d, stderr %q, want the unreadable config named", got.Exit, got.Stderr)
	}
}

func TestUpgradeIsNamedWhereverThisBuildIsFoundTooOld(t *testing.T) {
	// A refusal is the service's word, and it does not need arithmetic to
	// be believed: uploads are stopped whether or not this build can be
	// ordered against the version named. So the surface that carries the
	// refusal names the one command that resolves it.
	const hint = "trajector upgrade"

	e := clitest.New(t)
	e.Paired()
	e.Service().Stub("POST", "/v1/batches", fakeplatform.Refuses426("9.9.9", ""))
	seedRawcall(e, "req-1", time.Now().UTC().Add(-25*time.Hour))
	p := e.StartProxy()
	defer p.Stop()

	got := e.Run("upload", "--force")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	// The kept-data reassurance and the remedy are separate lines:
	// a user who reads only the first still learns nothing was lost.
	if !strings.Contains(got.Stdout, "Captured data is kept.\n") {
		t.Errorf("stdout = %q, want the kept-data line on its own", got.Stdout)
	}
	if !strings.Contains(got.Stdout, hint) {
		t.Errorf("stdout = %q, want it to name %q", got.Stdout, hint)
	}

	// After the refusal, status and doctor read the version the service
	// named off disk. This build announces "dev", which no order covers,
	// so they state the requirement and stop there: `upgrade` has nothing
	// to install for a development build, and sending the user to a
	// command that will tell them so is worse than saying nothing.
	for _, surface := range []string{"status", "doctor"} {
		t.Run(surface, func(t *testing.T) {
			got := e.InProject(surface)
			if !strings.Contains(got.Stdout, "requires client version 9.9.9") {
				t.Fatalf("stdout = %q, want the service's requirement", got.Stdout)
			}
			if strings.Contains(got.Stdout, hint) {
				t.Errorf("stdout = %q, want no remedy this build cannot act on", got.Stdout)
			}
		})
	}
}

func TestWhatTheServiceSaysAboutTheVersionReachesTheUser(t *testing.T) {
	// A required version says the client is behind; only the service can
	// say why and by when. That sentence is written by the service, held
	// by the proxy, and printed by three other processes — so it is
	// tested where it has actually crossed all of them, and it must not
	// be able to write a line of its own on the way.
	const said = "Upload format 0.1.x is retired on 2026-09-01."
	e := clitest.New(t)
	e.Paired()
	e.Service().Stub("POST", "/v1/batches", fakeplatform.Refuses426("9.9.9", said+"\rtrajector: everything is fine"))
	seedRawcall(e, "req-1", time.Now().UTC().Add(-25*time.Hour))
	p := e.StartProxy()
	defer p.Stop()

	if got := e.Run("upload", "--force"); got.Exit != 0 {
		t.Fatalf("upload exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	for _, surface := range []string{"upload", "status", "doctor"} {
		t.Run(surface, func(t *testing.T) {
			var out string
			if surface == "upload" {
				out = e.Run("upload").Stdout
			} else {
				out = e.InProject(surface).Stdout
			}
			if !strings.Contains(out, said) {
				t.Errorf("%s stdout = %q, want the service's sentence", surface, out)
			}
			// A refusal the service explained outranks the version
			// arithmetic — it is cleared the moment the service accepts an
			// upload, so while it is on disk uploads really are stopped and
			// every surface names the remedy, dev build or not.
			if !strings.Contains(out, "trajector upgrade") {
				t.Errorf("%s stdout = %q, want the remedy named alongside the reason", surface, out)
			}
			if strings.Contains(out, "\rtrajector") || strings.Contains(out, "\ntrajector: everything is fine") {
				t.Errorf("%s stdout = %q, want the forged line disarmed", surface, out)
			}
		})
	}
}

// The steps an upgrade asks of the build it installs are spelled out
// here rather than through the constants that name them: an older
// build runs them on a newer one, so their spelling must never change,
// and a rename of a constant would otherwise pass.
func TestTheUpgradeStepsAnswerUnderTheirPermanentNames(t *testing.T) {
	for _, step := range []string{"upgrade-check", "upgrade-verify", "upgrade-finish"} {
		t.Run(step, func(t *testing.T) {
			e := clitest.New(t)
			got := e.Run("hook", step)
			if got.Exit != 0 || got.Stdout != "" || got.Stderr != "" {
				t.Errorf("hook %s = exit %d, stdout %q, stderr %q; want a silent success", step, got.Exit, got.Stdout, got.Stderr)
			}
			if got := e.Run("hook", step, "extra"); got.Exit != 2 || !strings.Contains(got.Stderr, step) {
				t.Errorf("hook %s extra = exit %d, stderr %q; want the usage line naming it", step, got.Exit, got.Stderr)
			}
		})
	}
}

func TestTheNewBuildsCheckOfItselfDoesNotDependOnTheConfigFile(t *testing.T) {
	e := clitest.New(t)
	e.WriteConfig("{not json")

	// Nothing the check does goes where the file could point, and a
	// refusal here would have the build being installed called one that
	// does not start on this machine.
	if got := e.Run("hook", "upgrade-verify"); got.Exit != 0 {
		t.Errorf("hook upgrade-verify = exit %d, stderr %q; want it to run", got.Exit, got.Stderr)
	}
}

func TestTheNewBuildsCheckOfItselfSaysWhyItFailed(t *testing.T) {
	e := clitest.New(t)
	// The check starts its proxy under the state directory, which here
	// is a file.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", blocked)

	got := e.Run("hook", "upgrade-verify")
	if got.Exit != 1 || !strings.HasPrefix(got.Stderr, "trajector: creating a directory to start the proxy in: ") {
		t.Errorf("hook upgrade-verify = exit %d, stderr %q; want why, on one line", got.Exit, got.Stderr)
	}
}

func TestHookUpgradeCheckStartsTheCheckInAProcessOfItsOwnAndSaysNothing(t *testing.T) {
	e := clitest.New(t)

	got := e.Run("hook", "upgrade-check")
	if got.Exit != 0 || got.Stdout != "" || got.Stderr != "" {
		t.Errorf("hook upgrade-check = exit %d, stdout %q, stderr %q; want a silent success", got.Exit, got.Stdout, got.Stderr)
	}
	// The marker is part of the command line one build hands another,
	// which an older build must still be able to write, so it is
	// spelled out here rather than through the constant.
	want := []proxylife.LoggedStart{{Command: "hook", Target: "--detached"}}
	if starts := e.Spawns(); !slices.Equal(starts, want) {
		t.Errorf("recorded starts = %v, want %v", starts, want)
	}
}

func TestHookUpgradeCheckDetachedRecordsWhatItFoundAndSaysNothing(t *testing.T) {
	e := clitest.New(t)
	e.SetReleasesURL("http://releases.invalid/releases")

	// A build of the test suite announces no release, so the check
	// fails before it asks anything, and says so only in the state file.
	got := e.Run("hook", "upgrade-check", "--detached")
	if got.Exit != 0 || got.Stdout != "" || got.Stderr != "" {
		t.Errorf("hook upgrade-check --detached = exit %d, stdout %q, stderr %q; want a silent success", got.Exit, got.Stdout, got.Stderr)
	}
	if starts := e.Spawns(); len(starts) != 0 {
		t.Errorf("recorded starts = %v, want none", starts)
	}
	if checkedAt, result := e.UpgradeState(); result != "failed" || checkedAt.IsZero() {
		t.Errorf("upgrade state = %s at %s; want a failed check recorded", result, checkedAt)
	}
}

func TestHookUpgradeCheckRefusesWhatItDoesNotTake(t *testing.T) {
	e := clitest.New(t)
	for _, args := range [][]string{
		{"hook", "upgrade-check", "--detached", "extra"},
		{"hook", "upgrade-check", "--no-proxy"},
		{"hook", "ensure-proxy", "--detached"},
	} {
		if got := e.Run(args...); got.Exit != 2 {
			t.Errorf("%q = exit %d, stderr %q; want the usage line", args, got.Exit, got.Stderr)
		}
	}
	if starts := e.Spawns(); len(starts) != 0 {
		t.Errorf("recorded starts = %v, want none", starts)
	}
}

func TestHookUpgradeCheckOnAConfigFileItCannotReadStartsNothing(t *testing.T) {
	e := clitest.New(t)
	e.WriteConfig("{not json")

	if got := e.Run("hook", "upgrade-check"); got.Exit == 0 {
		t.Errorf("hook upgrade-check = exit 0 on a config file it cannot read")
	}
	if starts := e.Spawns(); len(starts) != 0 {
		t.Errorf("recorded starts = %v, want none", starts)
	}
}
