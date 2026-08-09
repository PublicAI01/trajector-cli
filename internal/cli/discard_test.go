package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
)

// quarantineBatch plants one batch in the rejected store, whose layout
// is a documented product contract.
func quarantineBatch(t *testing.T, e *clitest.Env, batchID string, requestIDs ...string) {
	t.Helper()
	dir := filepath.Join(e.Layout().RejectedDir(), batchID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range requestIDs {
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reason, err := json.Marshal(map[string]any{"batch_id": batchID, "records": len(requestIDs)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reason.json"), reason, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorDiscardUsage(t *testing.T) {
	e := clitest.New(t)
	for _, args := range [][]string{
		{"doctor", "discard"},
		{"doctor", "discard", "b-1", "b-2"},
		{"doctor", "discard", "--all", "b-1"},
	} {
		got := e.Run(args...)
		if got.Exit != 2 || !strings.Contains(got.Stderr, "usage: trajector doctor discard") {
			t.Errorf("%v -> %+v, want a usage error", args, got)
		}
	}
}

func TestDoctorSubcommandUsageSeparatesRequeueFromDiscard(t *testing.T) {
	e := clitest.New(t)
	got := e.Run("doctor", "frobnicate")
	if got.Exit != 2 {
		t.Fatalf("got %+v, want a usage error", got)
	}
	for _, want := range []string{"requeue", "discard", "upload again", "for good"} {
		if !strings.Contains(got.Stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", got.Stderr, want)
		}
	}
}

func TestDoctorDiscardAsksBeforeDeleting(t *testing.T) {
	e := clitest.New(t)
	quarantineBatch(t, e, "b-poison", "req-1")

	got := e.RunInput("n\n", "doctor", "discard", "b-poison")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "[y/N]") {
		t.Errorf("stdout = %q, want the confirmation question", got.Stdout)
	}
	if _, err := os.Stat(filepath.Join(e.Layout().RejectedDir(), "b-poison")); err != nil {
		t.Errorf("the batch was deleted after a no: %v", err)
	}
}

func TestDoctorDiscardDeletesAConfirmedBatch(t *testing.T) {
	e := clitest.New(t)
	quarantineBatch(t, e, "b-poison", "req-1", "req-2")

	got := e.RunInput("y\n", "doctor", "discard", "b-poison")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "2 rawcall(s)") {
		t.Errorf("stdout = %q, want the deletion count", got.Stdout)
	}
	if _, err := os.Stat(filepath.Join(e.Layout().RejectedDir(), "b-poison")); !os.IsNotExist(err) {
		t.Errorf("the batch survived a confirmed discard (stat: %v)", err)
	}
}

func TestDoctorDiscardUnknownBatchExitsOne(t *testing.T) {
	e := clitest.New(t)
	got := e.Run("doctor", "discard", "b-missing", "--yes")
	if got.Exit != 1 || !strings.Contains(got.Stderr, "b-missing") {
		t.Errorf("got %+v, want the unknown batch named and a failing exit code", got)
	}
}

func TestDoctorDiscardAllOnAnEmptyStore(t *testing.T) {
	e := clitest.New(t)
	got := e.Run("doctor", "discard", "--all")
	if got.Exit != 0 || !strings.Contains(got.Stdout, "No rejected batches") {
		t.Errorf("got %+v, want a clean nothing-to-do report", got)
	}
}
