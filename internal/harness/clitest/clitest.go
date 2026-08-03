// Package clitest drives the trajector CLI in-process against an
// isolated environment: a fresh HOME with all user-directory variables
// pointed inside a temp dir, so no test can read or write the
// developer's real trajector state.
package clitest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// Env is an isolated environment for one test.
type Env struct {
	t       *testing.T
	home    string
	project string
}

// New isolates the process environment and returns the harness. Every
// variable the CLI reads must be pinned here; add new variables in this
// one place when commands grow new environment inputs.
func New(t *testing.T) *Env {
	t.Helper()
	home := t.TempDir()
	// The project directory is named, not left as the numeric temp-dir
	// leaf: tests assert that hashes never contain the path, and a
	// two-digit leaf matches a hex digest by chance.
	project := filepath.Join(t.TempDir(), "sample-project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Env{t: t, home: home, project: project}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("ANTHROPIC_BASE_URL", "")
	// The file token backend keeps tests away from the developer's OS
	// keyring, and the unroutable service URL fails fast if a test
	// forgets to point at a fake platform server.
	t.Setenv("TRAJECTOR_TOKEN_STORE", "file")
	t.Setenv("TRAJECTOR_PLATFORM_URL", "http://127.0.0.1:1")
	t.Setenv("TRAJECTOR_PROXY_ADDR", "")
	return e
}

// Home is the temp directory standing in for the user's home.
func (e *Env) Home() string { return e.home }

// Project is the temp directory standing in for a working project.
func (e *Env) Project() string { return e.project }

// CanonicalRoot is the project root as the CLI resolves it, which is
// what every project hash and routing entry is keyed on.
func (e *Env) CanonicalRoot() string {
	e.t.Helper()
	root, err := consent.CanonicalRoot(e.project)
	if err != nil {
		e.t.Fatal(err)
	}
	return root
}

// ProjectHash is this project's identifier in stored records.
func (e *Env) ProjectHash() string { return consent.ProjectIDHash(e.CanonicalRoot()) }

// ProjectSettings is the settings file `trajector enable` injects.
func (e *Env) ProjectSettings() string {
	return claudesettings.ProjectLocalPath(e.CanonicalRoot())
}

// UserSettings is the settings file holding the discovery hook.
func (e *Env) UserSettings() string { return claudesettings.UserSettingsPath(e.home) }

// Layout is where this environment keeps its trajector files, resolved
// exactly as the CLI will resolve them.
func (e *Env) Layout() userdirs.Layout {
	e.t.Helper()
	layout, err := userdirs.Resolve(userdirs.Host())
	if err != nil {
		e.t.Fatal(err)
	}
	return layout
}

// Sandbox reads and seeds the routing table and spool this environment's
// CLI shares with a proxy.
func (e *Env) Sandbox() *proxytest.Sandbox { return proxytest.Open(e.t, e.Layout()) }

// Result captures one CLI invocation.
type Result struct {
	Exit   int
	Stdout string
	Stderr string
}

// Run executes the root command in-process with empty stdin.
func (e *Env) Run(args ...string) Result {
	return e.RunInput("", args...)
}

// RunInput executes the root command in-process, feeding input as
// stdin for interactive prompts.
func (e *Env) RunInput(input string, args ...string) Result {
	e.t.Helper()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(args, strings.NewReader(input), &stdout, &stderr)
	return Result{Exit: exit, Stdout: stdout.String(), Stderr: stderr.String()}
}

// InProject runs the command with the working directory set to the
// project dir, matching how users invoke project-scoped commands.
func (e *Env) InProject(args ...string) Result {
	return e.InProjectInput("", args...)
}

// InProjectInput is InProject with stdin input.
func (e *Env) InProjectInput(input string, args ...string) Result {
	e.t.Helper()
	e.t.Chdir(e.project)
	return e.RunInput(input, args...)
}
