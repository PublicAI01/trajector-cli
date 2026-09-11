package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/spool"
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
	userID, err := json.Marshal(map[string]string{"device_id": "d", "account_uuid": "a", "session_id": sessionID})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"model":    "claude-fable-5",
		"metadata": map[string]string{"user_id": string(userID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.Sandbox().SeedRawcall("req-"+sessionID[9:13], "hash-project", at, func(o *proxytest.Observation) {
		o.Request = body
	})
	sp, err := spool.Create(e.Layout().SpoolDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	capture := envelope.TranscriptCapture{
		ClientVersion: "test",
		Timestamp:     at.Format(time.RFC3339Nano),
		ProjectIDHash: "hash-project",
		Injection:     envelope.InjectionProxy,
	}
	seg := envelope.NewSegment(sessionID, "", 0, capture, `{"type":"user","message":{"role":"user","content":"hi"}}`+"\n")
	if err := sp.WriteSegment(seg); err != nil {
		t.Fatal(err)
	}
}

// sessionsHeld reports which sessions the spool still has anything
// for, counting rawcalls and records together.
func sessionsHeld(t *testing.T, e *clitest.Env) map[string]int {
	t.Helper()
	held := map[string]int{}
	for _, r := range e.Sandbox().Rawcalls() {
		if id, ok := spool.SessionIDFromUserID(r.SessionKey); ok {
			held[id]++
		}
	}
	sp, err := spool.Open(e.Layout().SpoolDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	err = sp.EachRecord(func(r spool.Record) error {
		held[r.SessionID]++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return held
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
	if !strings.Contains(got.Stdout, "Deleted 1 recorded call(s) and 1 session record(s).") {
		t.Errorf("stdout = %q, want both counts reported", got.Stdout)
	}
	if held := sessionsHeld(t, e); held[sessionOne] != 0 || held[sessionTwo] != 2 {
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
	if held := sessionsHeld(t, e); held[sessionOne] != 0 || held[sessionTwo] != 2 {
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
	if held := sessionsHeld(t, e); held[sessionOne] != 2 {
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
			if held := sessionsHeld(t, e); held[sessionOne] != 2 {
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
		"Deleted 0 recorded call(s) and 0 session record(s).\n"
	if got.Exit != 0 || got.Stdout != want || got.Stderr != "" {
		t.Errorf("got %+v\nwant exit 0, empty stderr, stdout:\n%q", got, want)
	}
}
