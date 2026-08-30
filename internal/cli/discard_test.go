package cli_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// quarantineBatch sets aside one batch of records that no longer read
// back as rawcalls, which is all these commands need to act on.
func quarantineBatch(t *testing.T, e *clitest.Env, batchID string, requestIDs ...string) {
	t.Helper()
	records := map[string][]byte{}
	for _, id := range requestIDs {
		records[id] = []byte(`{}`)
	}
	e.Sandbox().QuarantineBatch(proxytest.Rejection{BatchID: batchID}, records)
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
	if n := len(e.Sandbox().QuarantinedBatches()); n != 1 {
		t.Errorf("quarantine holds %d batch(es) after a no, want the batch kept", n)
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
	if n := len(e.Sandbox().QuarantinedBatches()); n != 0 {
		t.Errorf("quarantine holds %d batch(es) after a confirmed discard, want none", n)
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
