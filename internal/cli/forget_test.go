package cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

const (
	sessionOne = "0a1b2c3d-1111-4aaa-8aaa-000000000001"
	sessionTwo = "0a1b2c3d-2222-4bbb-8bbb-000000000002"
)

// seedSession stores one rawcall and one segment for a session, the
// two kinds of record forget must reach.
func seedSession(t *testing.T, e *clitest.Env, sessionID string) {
	t.Helper()
	at := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	e.Sandbox().SeedRawcall("req-"+sessionID[9:13], "hash-project", at, func(o *proxytest.Observation) {
		o.Request = proxytest.RequestBodyOfSession(t, sessionID)
	})
	e.Sandbox().SeedSegment(sessionID, "hash-project", at)
}

func TestForget_DefaultsToTheCurrentSession(t *testing.T) {
	e := clitest.New(t)
	t.Setenv(lifecycle.SessionIDEnv, sessionOne)
	seedSession(t, e, sessionOne)
	seedSession(t, e, sessionTwo)

	got := e.Run("forget")

	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Forgetting session "+sessionOne+":") {
		t.Errorf("stdout = %q, want the current session named", got.Stdout)
	}
	if !strings.Contains(got.Stdout, "Deleted 2 record(s) of session "+sessionOne+".") {
		t.Errorf("stdout = %q, want one count across every store", got.Stdout)
	}
	if held := e.Sandbox().SessionsHeld(); held[sessionOne] != 0 || held[sessionTwo] != 2 {
		t.Errorf("spool holds %v, want nothing of the current session and everything of the other", held)
	}
}

func TestForget_AcceptsAnExplicitID(t *testing.T) {
	e := clitest.New(t)
	t.Setenv(lifecycle.SessionIDEnv, sessionTwo)
	seedSession(t, e, sessionOne)
	seedSession(t, e, sessionTwo)

	got := e.Run("forget", sessionOne)

	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Forgetting session "+sessionOne+":") {
		t.Errorf("stdout = %q, want the named session, not the current one", got.Stdout)
	}
	if held := e.Sandbox().SessionsHeld(); held[sessionOne] != 0 || held[sessionTwo] != 2 {
		t.Errorf("spool holds %v, want the named session gone and the current one untouched", held)
	}
}

func TestForget_WithoutAnyIDExplainsHowToGiveOne(t *testing.T) {
	e := clitest.New(t)
	t.Setenv(lifecycle.SessionIDEnv, "")
	seedSession(t, e, sessionOne)

	got := e.Run("forget")

	if got.Exit != 2 {
		t.Fatalf("exit = %d, want 2 (stdout: %q, stderr: %q)", got.Exit, got.Stdout, got.Stderr)
	}
	for _, want := range []string{"usage: trajector forget <session-id>", "inside a Claude Code session", "pass the session id"} {
		if !strings.Contains(got.Stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", got.Stderr, want)
		}
	}
	if got.Stdout != "" {
		t.Errorf("stdout = %q, want nothing", got.Stdout)
	}
	if held := e.Sandbox().SessionsHeld(); held[sessionOne] != 2 {
		t.Errorf("spool holds %v, want everything untouched", held)
	}
}

func TestForget_RefusesWhatCannotBeASessionID(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "a path", args: []string{"forget", "../" + sessionOne}},
		{name: "whitespace inside", args: []string{"forget", "0a1b 2c3d"}},
		{name: "an empty argument", args: []string{"forget", ""}},
		{name: "two ids", args: []string{"forget", sessionOne, sessionTwo}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := clitest.New(t)
			t.Setenv(lifecycle.SessionIDEnv, sessionOne)
			seedSession(t, e, sessionOne)

			got := e.Run(tt.args...)

			if got.Exit != 2 || !strings.Contains(got.Stderr, "usage: trajector forget <session-id>") {
				t.Errorf("%v -> %+v, want a usage error", tt.args, got)
			}
			if got.Stdout != "" {
				t.Errorf("stdout = %q, want nothing announced", got.Stdout)
			}
			if held := e.Sandbox().SessionsHeld(); held[sessionOne] != 2 {
				t.Errorf("spool holds %v, want everything untouched", held)
			}
		})
	}
}

func TestForget_PrintsTheAgreedWording(t *testing.T) {
	e := clitest.New(t)
	t.Setenv(lifecycle.SessionIDEnv, "")

	got := e.Run("forget", sessionOne)

	want := "Forgetting session " + sessionOne + ": deleting its records that have not been uploaded yet from this machine. " +
		"Uploaded data is deleted from the Dashboard instead. " +
		"Recording of this session continues; only what was collected so far is gone.\n" +
		"Deleted 0 record(s) of session " + sessionOne + ".\n"
	if got.Exit != 0 || got.Stdout != want || got.Stderr != "" {
		t.Errorf("got %+v\nwant exit 0, empty stderr, stdout:\n%q", got, want)
	}
}

// quarantinedRawcallOf is one rawcall of a session as a rejected batch
// holds it.
func quarantinedRawcallOf(t *testing.T, sessionID string) []byte {
	t.Helper()
	return proxytest.Rawcall(t, "req-rejected-"+sessionID[9:13], "hash-project",
		time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC), func(o *proxytest.Observation) {
			o.Request = proxytest.RequestBodyOfSession(t, sessionID)
		})
}

func TestForget_ReachesTheRecordsAQuarantinedBatchHolds(t *testing.T) {
	e := clitest.New(t)
	t.Setenv(lifecycle.SessionIDEnv, "")
	e.Sandbox().QuarantineBatch(proxytest.Rejection{BatchID: "b-mixed"}, map[string][]byte{
		"req-rejected-1111": quarantinedRawcallOf(t, sessionOne),
		"req-rejected-2222": quarantinedRawcallOf(t, sessionTwo),
	})

	first := e.Run("forget", sessionOne)

	if first.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", first.Exit, first.Stderr)
	}
	if !strings.Contains(first.Stdout, "Deleted 1 record(s) of session "+sessionOne+".") {
		t.Errorf("stdout = %q, want the quarantined record counted", first.Stdout)
	}
	if ids := e.Sandbox().QuarantinedRecordIDs("b-mixed"); strings.Join(ids, ",") != "req-rejected-2222" {
		t.Errorf("batch holds %v, want only the other session's record", ids)
	}

	second := e.Run("forget", sessionTwo)

	if second.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", second.Exit, second.Stderr)
	}
	if !strings.Contains(second.Stdout, "Deleted 1 record(s) of session "+sessionTwo+".") {
		t.Errorf("stdout = %q, want the last quarantined record counted", second.Stdout)
	}
	if batches := e.Sandbox().QuarantinedBatches(); len(batches) != 0 {
		t.Errorf("quarantine holds %+v, want the emptied batch gone whole", batches)
	}
}
