package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
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
	batchDir := filepath.Join(e.Layout().RejectedDir(), "b-poison")
	if err := os.MkdirAll(batchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batchDir, "req-1.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reason, _ := json.Marshal(map[string]any{"batch_id": "b-poison", "records": 1})
	if err := os.WriteFile(filepath.Join(batchDir, "reason.json"), reason, 0o600); err != nil {
		t.Fatal(err)
	}

	got := e.InProject("doctor")
	if got.Exit != 1 {
		t.Fatalf("exit = %d, want 1 with a quarantined batch (stdout: %q)", got.Exit, got.Stdout)
	}
}

func TestDoctorBundleWritesAnArchiveInTheWorkingDirectory(t *testing.T) {
	e := clitest.New(t)
	got := e.InProject("doctor", "bundle")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	entries, err := os.ReadDir(e.Project())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range entries {
		if strings.HasPrefix(f.Name(), "trajector-doctor-") && strings.HasSuffix(f.Name(), ".tar.gz") {
			found = true
			if !strings.Contains(got.Stdout, f.Name()) {
				t.Errorf("stdout = %q, want the bundle name %s reported", got.Stdout, f.Name())
			}
		}
	}
	if !found {
		t.Errorf("no bundle written into the project directory; stdout = %q", got.Stdout)
	}
}

func TestDoctorRejectsUnknownSubcommands(t *testing.T) {
	e := clitest.New(t)
	got := e.Run("doctor", "frobnicate")
	if got.Exit != 2 || !strings.Contains(got.Stderr, "usage: trajector doctor") {
		t.Errorf("got %+v, want a usage error", got)
	}
}
