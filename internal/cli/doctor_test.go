package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestDoctorExitsZeroWhenClean(t *testing.T) {
	e := clitest.New(t)
	got := e.InProject("doctor")
	if got.Exit != 0 {
		t.Fatalf("exit = %d, want 0 (stdout: %q, stderr: %q)", got.Exit, got.Stdout, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Everything checks out") {
		t.Errorf("stdout = %q, want a clean summary", got.Stdout)
	}
}

func TestDoctorExitsOneWhenProblemsRemain(t *testing.T) {
	e := clitest.New(t)
	quarantineBatch(t, e, "b-poison", "req-1")

	got := e.InProject("doctor")
	if got.Exit != 1 {
		t.Fatalf("exit = %d, want 1 with a quarantined batch (stdout: %q)", got.Exit, got.Stdout)
	}
}

func TestDoctorSeparatesServiceRefusalsFromUnreadableRecords(t *testing.T) {
	e := clitest.New(t)
	e.Sandbox().QuarantineBatch(
		proxytest.Rejection{BatchID: "b-refused", Cause: proxytest.CauseRefused, Details: "413 Request Entity Too Large"},
		map[string][]byte{"req-1": []byte(`{}`)})
	e.Sandbox().QuarantineBatch(
		proxytest.Rejection{BatchID: "b-torn", Cause: proxytest.CauseUnreadable, Details: "bad envelope"},
		map[string][]byte{"req-2": []byte("not an envelope")})

	got := e.InProject("doctor")
	if got.Exit != 1 {
		t.Fatalf("exit = %d, want 1 with quarantined batches (stdout: %q)", got.Exit, got.Stdout)
	}
	for _, want := range []string{
		"b-refused", "413 Request Entity Too Large",
		"b-torn", "never sent: unreadable in the spool",
		"requeue <batch-id>",
		"Unreadable records cannot be requeued",
	} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", got.Stdout, want)
		}
	}
}

func TestDoctorBundleWritesAnArchiveInTheWorkingDirectory(t *testing.T) {
	e := clitest.New(t)
	e.At(time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC))
	got := e.InProject("doctor", "bundle")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	name := "trajector-doctor-20260807-100000.tar.gz"
	if _, err := os.Stat(filepath.Join(e.Project(), name)); err != nil {
		t.Errorf("no bundle at the pinned clock's name: %v (stdout: %q)", err, got.Stdout)
	}
	if !strings.Contains(got.Stdout, name) {
		t.Errorf("stdout = %q, want the bundle name %s reported", got.Stdout, name)
	}
}

func TestDoctorRejectsUnknownSubcommands(t *testing.T) {
	e := clitest.New(t)
	got := e.Run("doctor", "frobnicate")
	if got.Exit != 2 || !strings.Contains(got.Stderr, "usage: trajector doctor") {
		t.Errorf("got %+v, want a usage error", got)
	}
	got = e.Run("doctor", "bundle", "extra")
	if got.Exit != 2 || !strings.Contains(got.Stderr, "usage: trajector doctor bundle") {
		t.Errorf("got %+v, want a bundle usage error", got)
	}
}

// hookInDefaultClaudeDir writes a settings file into the default
// Claude Code configuration directory carrying a discovery hook of
// trajector's beside a hook of the user's own. The environment this
// harness builds names another configuration directory, so the file is
// one Claude Code does not read.
func hookInDefaultClaudeDir(t *testing.T, e *clitest.Env) string {
	t.Helper()
	path := filepath.Join(e.Home(), ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "/old/path/trajector hook discovery"}]},
      {"hooks": [{"type": "command", "command": "echo the user's own hook"}]}
    ]
  }
}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDoctorRemovesTheHookLeftInTheDirectoryClaudeCodeNoLongerReads(t *testing.T) {
	e := clitest.New(t)
	path := hookInDefaultClaudeDir(t, e)

	got := e.InProject("doctor")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stdout: %q, stderr: %q)", got.Exit, got.Stdout, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "removed a trajector hook left in ~/.claude/settings.json") {
		t.Errorf("stdout = %q, want the removal reported", got.Stdout)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hook discovery") {
		t.Errorf("settings file = %s, want trajector's hook gone", data)
	}
	if !strings.Contains(string(data), "echo the user's own hook") {
		t.Errorf("settings file = %s, want the user's own hook kept", data)
	}

	again := e.InProject("doctor")
	if again.Exit != 0 {
		t.Fatalf("second run exit = %d (stdout: %q)", again.Exit, again.Stdout)
	}
	if strings.Contains(again.Stdout, "~/.claude/settings.json") {
		t.Errorf("second run = %q, want nothing left to say about the file", again.Stdout)
	}
}
