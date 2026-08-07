package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
