package selfupdate_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// installBuild puts a real release build of version where this
// machine's binary lives, under the name the platform runs it by.
func installBuild(t *testing.T, version string) installed {
	t.Helper()
	return installBytes(t, fakereleases.BuildRelease(t, version))
}

// reportedVersion is what the binary at path prints for `version`, the
// line a user reads to learn which release they are on.
func reportedVersion(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), path, "version").Output()
	if err != nil {
		t.Fatalf("running %s version: %v", path, err)
	}
	return string(out)
}

// bytesOf is the binary on disk where this machine's binary lives.
func (m installed) bytesOf() []byte {
	m.t.Helper()
	data, err := os.ReadFile(m.path)
	if err != nil {
		m.t.Fatalf("the binary is gone: %v", err)
	}
	return data
}

// unusableRelease runs an upgrade from a real 0.1.0 build to a 0.2.0
// release whose binary is body, which does not start as 0.2.0, and
// checks what every such run owes the machine: the error says which
// release, the installed binary is byte for byte the one it was,
// nothing is left beside it, and the release is recorded as unusable
// here. It returns the recorded reason.
func unusableRelease(t *testing.T, body []byte) string {
	t.Helper()
	m := installBuild(t, "0.1.0")
	before := m.bytesOf()
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", body)

	_, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0)
	unusable, ok := errors.AsType[*selfupdate.UnusableError](err)
	if !ok {
		t.Fatalf("Upgrade = %v, want a release that does not start", err)
	}
	if unusable.Version != "0.2.0" {
		t.Errorf("the release that does not start is %q, want 0.2.0", unusable.Version)
	}
	if !bytes.Equal(m.bytesOf(), before) {
		t.Error("the installed binary changed")
	}
	if got := m.siblings(); len(got) != 1 {
		t.Errorf("a release that does not start left %v in the install directory", got)
	}
	state, err := selfupdate.LoadState(m.at("0.1.0").State)
	if err != nil {
		t.Fatal(err)
	}
	if state.Unusable["0.2.0"] != unusable.Reason || unusable.Reason == "" {
		t.Errorf("unusable versions are %v, want 0.2.0 recorded with %q", state.Unusable, unusable.Reason)
	}
	return unusable.Reason
}

func TestUpgradeReplacesOneReleaseBuildWithAnotherThatRuns(t *testing.T) {
	m := installBuild(t, "0.1.0")
	if got := reportedVersion(t, m.path); got != "trajector 0.1.0\n" {
		t.Fatalf("the installation reports %q before the upgrade, want trajector 0.1.0", got)
	}
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", fakereleases.BuildRelease(t, "0.2.0"))

	out, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if out.Kind != selfupdate.Upgraded || out.From != "0.1.0" || out.To != "0.2.0" {
		t.Errorf("outcome is %+v, want an upgrade from 0.1.0 to 0.2.0", out)
	}
	// The installed file is the release that was published, unpacked
	// and runnable: it answers as the new version, not the old one.
	if got := reportedVersion(t, m.path); got != "trajector 0.2.0\n" {
		t.Errorf("after the upgrade the installation reports %q, want trajector 0.2.0", got)
	}
}

func TestUpgrade_SmokeFailureLeavesBinaryUntouched(t *testing.T) {
	// The checksum matches — the pipeline published these bytes — but
	// they are no program at all, as a build for another platform or a
	// truncated upload would be.
	reason := unusableRelease(t, []byte("these bytes are not a program"))
	if !strings.HasPrefix(reason, "it cannot be run: ") {
		t.Errorf("reason = %q, want the system's refusal to run it", reason)
	}
	if strings.Contains(reason, ".incoming-") {
		t.Errorf("reason = %q names a staged file that no longer exists", reason)
	}
}

func TestUpgrade_ReleaseThatReportsAnotherVersionIsNotInstalled(t *testing.T) {
	// The asset name is right and the binary runs, but it is a different
	// build: installing it would leave the machine on a version nobody
	// chose and that the release order would offer to replace again.
	reason := unusableRelease(t, fakereleases.BuildRelease(t, "0.1.5"))
	if reason != `it reports itself as "trajector 0.1.5"` {
		t.Errorf("reason = %q, want the version it reported", reason)
	}
}

func TestUpgrade_ReleaseThatExitsWithAFailureIsNotInstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a script is not a program on Windows")
	}
	// It even prints the right line, but a version command that fails is
	// a binary that is missing something it needs.
	reason := unusableRelease(t, []byte("#!/bin/sh\necho trajector 0.2.0\nexit 3\n"))
	if reason != "it exited with exit status 3" {
		t.Errorf("reason = %q, want the exit status", reason)
	}
}

func TestUpgrade_ReleaseThatDoesNotAnswerIsNotInstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a script is not a program on Windows")
	}
	reason := unusableRelease(t, []byte("#!/bin/sh\nexec sleep 60\n"))
	if reason != "it did not answer within 5s" {
		t.Errorf("reason = %q, want the time it was given", reason)
	}
}

func TestUpgrade_ReleaseThatPrintsWithoutEndIsQuotedOnlyByItsStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a script is not a program on Windows")
	}
	reason := unusableRelease(t, []byte("#!/bin/sh\ni=0\nwhile [ $i -lt 5000 ]; do echo trajector 0.2.0 and then some; i=$((i+1)); done\n"))
	if len(reason) > 300 || !strings.HasPrefix(reason, `it reports itself as "trajector 0.2.0 and then some"`) {
		t.Errorf("reason = %q, want the first line it printed", reason)
	}
}

func TestUpgrade_ManualUpgradeRetriesAReleaseRecordedAsUnusable(t *testing.T) {
	m := installBuild(t, "0.1.0")
	broken := fakereleases.New(t)
	broken.Publish(t, "0.2.0", []byte("these bytes are not a program"))
	if _, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), broken.IndexURL(), 0); err == nil {
		t.Fatal("a release that does not start was installed")
	}

	// The release was republished fixed under the same version. A run
	// asked for by hand tries it again rather than trusting the note.
	fixed := fakereleases.New(t)
	fixed.Publish(t, "0.2.0", fakereleases.BuildRelease(t, "0.2.0"))
	out, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), fixed.IndexURL(), 0)
	if err != nil || out.Kind != selfupdate.Upgraded {
		t.Fatalf("Upgrade = %+v, %v; want the release tried again and installed", out, err)
	}
	state, err := selfupdate.LoadState(m.at("0.2.0").State)
	if err != nil {
		t.Fatal(err)
	}
	// The machine is on 0.2.0 now, so a note about 0.2.0 is about a
	// version it has passed.
	if state.Installed != "0.2.0" || len(state.Unusable) != 0 || state.Error != "" {
		t.Errorf("state is %+v, want 0.2.0 installed and nothing unusable or failed", state)
	}
}
