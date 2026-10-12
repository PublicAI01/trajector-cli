package fakereleases

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// mainPackage is the command the release pipeline builds, and
// versionSymbol is the variable its ldflags stamp the version into
// (.goreleaser.yaml). A rename of either leaves a build that reports
// the wrong version, which the version check in any test installing
// one reports.
const (
	mainPackage   = "github.com/PublicAI01/trajector-cli/cmd/trajector"
	versionSymbol = "github.com/PublicAI01/trajector-cli/internal/cli.version"
)

// builds holds one build per version for the life of the test process.
// Linking the CLI takes seconds; every test after the first that asks
// for a version gets the same bytes.
var (
	buildsMu sync.Mutex
	builds   = map[string]func() ([]byte, error){}
)

// BuildRelease is a real trajector binary for this machine's platform
// that reports version, built the way the release pipeline builds one:
// -X stamps the version into the variable the version command reads,
// with the pipeline's -trimpath, -s -w and CGO_ENABLED=0. Publish it to
// make it a release an upgrade picks and installs, or write it where a
// machine's binary lives to make it the installation an upgrade starts
// from.
//
// Each version is built once per test process, and every call for it
// returns the same slice: callers read it and must not modify it.
func BuildRelease(t *testing.T, version string) []byte {
	t.Helper()
	buildsMu.Lock()
	build, ok := builds[version]
	if !ok {
		build = sync.OnceValues(func() ([]byte, error) { return buildRelease(version) })
		builds[version] = build
	}
	buildsMu.Unlock()

	binary, err := build()
	if err != nil {
		t.Fatalf("fakereleases: building release %s: %v", version, err)
	}
	return binary
}

// buildRelease links the CLI into a directory of its own, reads the
// binary back, and removes the directory: the bytes outlive the test
// that first asked for them, and nothing is left on disk.
//
// The go command found on PATH is the one running these tests: go test
// puts its own toolchain's bin directory first on the PATH it hands
// the test process. The build belongs to no one test — its result is
// shared by all of them — so no test's context can cancel it.
func buildRelease(version string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "fakereleases-build-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	out := filepath.Join(dir, "trajector")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	// The platform is the one this test binary runs on, which is the
	// one an upgrade selects an archive for. VCS stamping is off: the
	// version comes from -X, and a checkout git cannot read must not
	// fail the build.
	cmd := exec.CommandContext(context.Background(), "go", "build",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags", "-s -w -X "+versionSymbol+"="+version,
		"-o", out,
		mainPackage,
	)
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+runtime.GOOS,
		"GOARCH="+runtime.GOARCH,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go build: %w\n%s", err, output)
	}
	return fsatomic.ReadFile(out)
}
