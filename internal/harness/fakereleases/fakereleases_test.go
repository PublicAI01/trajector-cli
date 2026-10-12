package fakereleases_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
)

// version is the one release every test here builds, so the package
// pays for one link however many tests ask for it.
const version = "0.0.1"

func TestBuildReleaseIsABinaryThatReportsItsVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trajector")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, fakereleases.BuildRelease(t, version), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := exec.CommandContext(t.Context(), path, "version").Output()
	if err != nil {
		t.Fatalf("running the build: %v", err)
	}
	// What the version command prints is the stamp itself: a build whose
	// -X named a variable that is not there would report "dev" here.
	if got, want := string(out), "trajector "+version+"\n"; got != want {
		t.Errorf("the build reports %q, want %q", got, want)
	}
}

func TestBuildReleaseBuildsEachVersionOncePerProcess(t *testing.T) {
	first := fakereleases.BuildRelease(t, version)
	second := fakereleases.BuildRelease(t, version)

	// A second build would be a second slice, even with identical
	// content.
	if &first[0] != &second[0] {
		t.Error("asking for the same version twice built it twice")
	}
}
