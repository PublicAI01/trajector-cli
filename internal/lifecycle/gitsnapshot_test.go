package lifecycle_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

// gitProject makes this device's project a repository of the test's
// own and grants it, so the machine has something to observe.
func (e *env) gitProject() {
	e.t.Helper()
	e.enableProject()
	e.repo = proxytest.NewGitRepo(e.t, e.project)
}

func (e *env) gitCommit(name, content string) string {
	e.t.Helper()
	return e.repo.Commit(name, content)
}

// observe runs one observation with the hook input a session of the
// given event would have written.
func (e *env) observe(event string, extra map[string]any) {
	e.t.Helper()
	in := map[string]any{
		"session_id":      "0a1b2c3d-1111-4aaa-8aaa-000000000001",
		"cwd":             e.project,
		"hook_event_name": event,
	}
	for k, v := range extra {
		in[k] = v
	}
	data, err := json.Marshal(in)
	if err != nil {
		e.t.Fatal(err)
	}
	e.machine().ObserveGitSnapshot(e.project, lifecycle.ReadHookInput(strings.NewReader(string(data))))
}

// observations is every git snapshot the spool holds.
func (e *env) observations() []envelope.GitSnapshot {
	e.t.Helper()
	return e.sandbox.GitSnapshots()
}

func TestObserveGitSnapshotStatesWhatThisClientKnowsAboutItself(t *testing.T) {
	e := newEnv(t)
	e.gitProject()
	head := e.gitCommit("a.txt", "one")

	e.observe("SessionStart", nil)

	got := e.observations()
	if len(got) != 1 {
		t.Fatalf("spool holds %d observations, want one", len(got))
	}
	snap := got[0]
	if snap.Capture.ClientVersion != e.deps.Version || snap.Capture.ProjectIDHash != proxytest.ProjectIDHash(e.canonicalRoot()) {
		t.Errorf("capture = %+v, want this build and this project", snap.Capture)
	}
	if snap.Capture.Injection != envelope.InjectionProxy {
		t.Errorf("injection = %q, want the shape the grant records", snap.Capture.Injection)
	}
	if snap.Capture.Timestamp != e.deps.Now().UTC().Format(time.RFC3339Nano) {
		t.Errorf("timestamp = %q, want the moment of observation", snap.Capture.Timestamp)
	}
	if snap.SessionID != "0a1b2c3d-1111-4aaa-8aaa-000000000001" || snap.Head != head {
		t.Errorf("observation = %+v, want the session it ran for at %s", snap, head)
	}
}

func TestObserveGitSnapshotRemembersOnlyWhatItStored(t *testing.T) {
	e := newEnv(t)
	e.gitProject()
	first := e.gitCommit("a.txt", "one")
	e.observe("SessionStart", nil)

	// A tool use that made no commit stores nothing, so what the next
	// session-scoped observation compares against must not move either.
	e.gitCommit("a.txt", "two")
	e.observe("PostToolUse", map[string]any{
		"tool_input":    map[string]any{"command": "ls"},
		"tool_response": map[string]any{"stdout": ""},
	})
	e.observe("SessionEnd", nil)

	got := e.observations()
	if len(got) != 2 {
		t.Fatalf("spool holds %d observations, want one per session hook", len(got))
	}
	var closing envelope.GitSnapshot
	for _, snap := range got {
		if snap.Trigger == envelope.TriggerSessionEnd {
			closing = snap
		}
	}
	if closing.Base == nil || *closing.Base != first {
		t.Errorf("base = %v, want the commit the last stored observation saw (%s)", closing.Base, first)
	}
}

func TestObserveGitSnapshotStopsWhereRecordingStops(t *testing.T) {
	tests := []struct {
		name string
		stop func(e *env)
	}{
		{
			name: "a project that was never enabled",
			stop: func(e *env) { e.sandbox.RevokeProject(e.canonicalRoot(), "2026-08-02T12:00:00Z") },
		},
		{
			name: "a device-wide pause",
			stop: func(e *env) { e.sandbox.Pause(proxytest.PauseSignedOut) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.gitProject()
			e.gitCommit("a.txt", "one")
			tt.stop(e)

			e.observe("SessionStart", nil)
			if got := e.observations(); len(got) != 0 {
				t.Errorf("spool holds %+v, want nothing observed", got)
			}
		})
	}
}

func TestDisableForgetsTheCommitTheProjectWasLastObservedAt(t *testing.T) {
	e := newEnv(t)
	e.gitProject()
	e.gitCommit("a.txt", "one")
	e.observe("SessionStart", nil)
	head := e.gitCommit("a.txt", "two")

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatal(err)
	}
	e.enableProject()
	e.observe("SessionStart", nil)

	var opening envelope.GitSnapshot
	for _, snap := range e.observations() {
		if snap.Head == head {
			opening = snap
		}
	}
	if opening.Head != head {
		t.Fatalf("the re-enabled project was not observed: %+v", e.observations())
	}
	if opening.Base != nil {
		t.Errorf("base = %v, want nothing remembered across a disable", *opening.Base)
	}
}
