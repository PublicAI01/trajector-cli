package selfupdate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// installBuild puts a real release build of version where this
// machine's binary lives, under the name the platform runs it by.
func installBuild(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trajector")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, fakereleases.BuildRelease(t, version), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
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

func TestUpgradeReplacesOneReleaseBuildWithAnotherThatRuns(t *testing.T) {
	execPath := installBuild(t, "0.1.0")
	if got := reportedVersion(t, execPath); got != "trajector 0.1.0\n" {
		t.Fatalf("the installation reports %q before the upgrade, want trajector 0.1.0", got)
	}
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", fakereleases.BuildRelease(t, "0.2.0"))

	out, err := selfupdate.Upgrade(t.Context(), execPath, "0.1.0", releases.IndexURL())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if out.Kind != selfupdate.Upgraded || out.From != "0.1.0" || out.To != "0.2.0" {
		t.Errorf("outcome is %+v, want an upgrade from 0.1.0 to 0.2.0", out)
	}
	// The installed file is the release that was published, unpacked
	// and runnable: it answers as the new version, not the old one.
	if got := reportedVersion(t, execPath); got != "trajector 0.2.0\n" {
		t.Errorf("after the upgrade the installation reports %q, want trajector 0.2.0", got)
	}
}
