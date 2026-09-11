package lifecycle_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

const (
	sessionOne = "0a1b2c3d-1111-4aaa-8aaa-000000000001"
	sessionTwo = "0a1b2c3d-2222-4bbb-8bbb-000000000002"
)

// requestIDOf names the one rawcall a test seeds for a session after
// the part of the id that differs between the two sessions.
func requestIDOf(sessionID string) string { return "req-" + sessionID[9:13] }

// forgetIntroFor is the wording a user is promised, spelled out in
// full here so the test cannot inherit a drift from the code.
func forgetIntroFor(sessionID string) string {
	return "Forgetting session " + sessionID + ": deleting its records that have not been uploaded yet from this machine. " +
		"Uploaded data is deleted from the Dashboard instead. " +
		"Recording of this session continues; only what was collected so far is gone.\n"
}

func seedSessionRawcall(e *env, requestID, sessionID string) {
	e.t.Helper()
	e.sandbox.SeedRawcall(requestID, "hash-project", e.deps.Now(), func(o *proxytest.Observation) {
		o.Request = proxytest.RequestBodyOfSession(e.t, sessionID)
	})
}

// seedSessionRecords stores one segment of the session's own file and
// one snapshot of a sub-agent's, both under the session's id.
func seedSessionRecords(e *env, sessionID string) (segmentID, snapshotID string) {
	e.t.Helper()
	at := e.deps.Now()
	return e.sandbox.SeedSegment(sessionID, "hash-project", at),
		e.sandbox.SeedMetaSnapshot(sessionID, "hash-project", at)
}

// spoolHeld lists what the spool still holds, rawcalls by request id
// and records by record id.
func spoolHeld(e *env) (rawcalls, records []string) {
	e.t.Helper()
	for _, r := range e.sandbox.Rawcalls() {
		rawcalls = append(rawcalls, r.RequestID)
	}
	for _, r := range e.sandbox.Records() {
		records = append(records, r.ID)
	}
	return rawcalls, records
}

func TestForget_DeletesBothKindsAndLeavesOtherSessions(t *testing.T) {
	tests := []struct {
		name          string
		forget, other string
	}{
		{name: "the first of two sessions", forget: sessionOne, other: sessionTwo},
		{name: "the second of two sessions", forget: sessionTwo, other: sessionOne},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			seedSessionRawcall(e, requestIDOf(tt.forget), tt.forget)
			seedSessionRawcall(e, requestIDOf(tt.other), tt.other)
			seedSessionRecords(e, tt.forget)
			otherSegment, otherSnapshot := seedSessionRecords(e, tt.other)

			if err := e.machine().Forget(tt.forget, e.io()); err != nil {
				t.Fatalf("forget: %v\nstdout: %s", err, e.stdout)
			}

			if !strings.Contains(e.stdout.String(), "Deleted 1 recorded call(s) and 2 session record(s).\n") {
				t.Errorf("stdout = %q, want both counts reported", e.stdout)
			}
			rawcalls, records := spoolHeld(e)
			if want := []string{requestIDOf(tt.other)}; strings.Join(rawcalls, ",") != strings.Join(want, ",") {
				t.Errorf("rawcalls left = %v, want %v", rawcalls, want)
			}
			if len(records) != 2 || !(strings.Contains(strings.Join(records, ","), otherSegment) && strings.Contains(strings.Join(records, ","), otherSnapshot)) {
				t.Errorf("records left = %v, want exactly the other session's %s and %s", records, otherSegment, otherSnapshot)
			}
		})
	}
}

func TestForget_UploadedRecordsAreNotAffected(t *testing.T) {
	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", ackBatch(nil))
	servedProxy(t, e)
	seedSessionRawcall(e, "req-uploaded", sessionOne)
	segmentID, snapshotID := seedSessionRecords(e, sessionOne)
	m := e.machine()
	if err := m.Upload(true, e.io()); err != nil {
		t.Fatal(err)
	}
	if rawcalls, records := spoolHeld(e); len(rawcalls)+len(records) != 0 {
		t.Fatalf("precondition: spool still holds %v and %v after an acknowledged upload", rawcalls, records)
	}
	e.stdout.Reset()

	if err := m.Forget(sessionOne, e.io()); err != nil {
		t.Fatalf("forget: %v\nstdout: %s", err, e.stdout)
	}

	if !strings.Contains(e.stdout.String(), "Deleted 0 recorded call(s) and 0 session record(s).\n") {
		t.Errorf("stdout = %q, want nothing deleted, and said so", e.stdout)
	}
	var uploaded strings.Builder
	for _, r := range e.service.Requests() {
		parts, err := fakeplatform.Parts(r)
		if err != nil {
			t.Fatalf("reading upload request: %v", err)
		}
		for _, part := range parts {
			uploaded.Write(part)
		}
	}
	for _, want := range []string{"req-uploaded", segmentID, snapshotID} {
		if !strings.Contains(uploaded.String(), want) {
			t.Errorf("the service no longer holds %s after forget; uploaded data must be untouched", want)
		}
	}
}

func TestForget_PrintsTheAgreedWording(t *testing.T) {
	e := newEnv(t)
	if err := e.machine().Forget(sessionOne, e.io()); err != nil {
		t.Fatal(err)
	}
	want := forgetIntroFor(sessionOne) + "Deleted 0 recorded call(s) and 0 session record(s).\n"
	if got := e.stdout.String(); got != want {
		t.Errorf("stdout =\n%q\nwant\n%q", got, want)
	}
	if e.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", e.stderr)
	}
}

func TestForget_CurrentSessionIsReadFromTheEnvironment(t *testing.T) {
	e := newEnv(t)
	if got := e.machine().CurrentSessionID(); got != "" {
		t.Fatalf("CurrentSessionID = %q outside a session, want empty", got)
	}
	e.environ[lifecycle.SessionIDEnv] = sessionTwo
	if got := e.machine().CurrentSessionID(); got != sessionTwo {
		t.Errorf("CurrentSessionID = %q, want %q", got, sessionTwo)
	}
}

func TestForget_RefusesAnEmptyIDBeforeSayingAnything(t *testing.T) {
	e := newEnv(t)
	seedSessionRawcall(e, "req-kept", sessionOne)
	if err := e.machine().Forget("", e.io()); err == nil {
		t.Fatal("forget with no id was accepted")
	}
	if e.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing announced for a refused id", e.stdout)
	}
	if rawcalls, _ := spoolHeld(e); len(rawcalls) != 1 {
		t.Errorf("rawcalls left = %v, want the one seeded untouched", rawcalls)
	}
}
