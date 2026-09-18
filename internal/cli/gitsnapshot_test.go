package cli_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// gitEnv is a hook environment whose project is a git repository of
// the test's own.
type gitEnv struct {
	*hookEnv
	*proxytest.GitRepo
	t *testing.T
}

func newGitEnv(t *testing.T) *gitEnv {
	t.Helper()
	g := newUnenabledGitEnv(t)
	g.enabled()
	return g
}

// newUnenabledGitEnv is newGitEnv without the grant, for the tests
// about a project nobody enabled.
func newUnenabledGitEnv(t *testing.T) *gitEnv {
	t.Helper()
	h := newHookEnv(t)
	return &gitEnv{hookEnv: h, GitRepo: proxytest.NewGitRepo(t, h.Project()), t: t}
}

// hookInput is the JSON a session writes on a hook's stdin. The members
// a tool's hook carries are added by the caller.
func (g *gitEnv) hookInput(event string, extra map[string]any) string {
	g.t.Helper()
	in := map[string]any{
		"session_id":      "0f1e2d3c-1111-4aaa-8aaa-000000000001",
		"transcript_path": "",
		"cwd":             g.Project(),
		"hook_event_name": event,
	}
	for k, v := range extra {
		in[k] = v
	}
	data, err := json.Marshal(in)
	if err != nil {
		g.t.Fatal(err)
	}
	return string(data)
}

// observations is every git snapshot the spool holds.
func (g *gitEnv) observations() []envelope.GitSnapshot {
	g.t.Helper()
	return g.Sandbox().GitSnapshots()
}

// only is the one observation the spool holds.
func (g *gitEnv) only() envelope.GitSnapshot {
	g.t.Helper()
	got := g.observations()
	if len(got) != 1 {
		g.t.Fatalf("spool holds %d observations, want exactly one", len(got))
	}
	return got[0]
}

// toolUse is the stdin members a shell tool's hook carries.
func toolUse(command string, reportedCommit bool) map[string]any {
	response := map[string]any{"stdout": "", "interrupted": false}
	if reportedCommit {
		response["gitOperation"] = map[string]any{
			"commit": map[string]any{"sha": "abc1234", "kind": "created", "branch": "main"},
		}
	}
	return map[string]any{
		"tool_input":    map[string]any{"command": command},
		"tool_response": response,
	}
}

func TestGitSnapshot_SessionStartObservesTheRepositoryAndSaysNothing(t *testing.T) {
	g := newGitEnv(t)
	head := g.Commit("a.txt", "one")

	assertSilentSuccess(t, g.InProjectInput(g.hookInput("SessionStart", nil), "hook", "ensure-proxy"))

	snap := g.only()
	if snap.HookEvent != "SessionStart" || snap.Trigger != envelope.TriggerSessionStart {
		t.Errorf("observation = %s/%s, want the event it ran under", snap.HookEvent, snap.Trigger)
	}
	if snap.Head != head || snap.Branch != "main" {
		t.Errorf("head/branch = %s/%s, want %s/main", snap.Head, snap.Branch, head)
	}
	// The first observation of a project has nothing to compare against.
	if snap.Base != nil || len(snap.Changed) != 0 || snap.ChangedCount != 0 {
		t.Errorf("first observation = %+v, want nothing compared against", snap)
	}
	if snap.Parent != nil {
		t.Errorf("parent = %v, want none at a root commit", *snap.Parent)
	}
}

func TestGitSnapshot_SessionEndComparesAgainstWhatWasSeenAtTheStart(t *testing.T) {
	g := newGitEnv(t)
	g.Commit("a.txt", "one")
	assertSilentSuccess(t, g.InProjectInput(g.hookInput("SessionStart", nil), "hook", "ensure-proxy"))

	// Two commits nothing observed in between: the span they make is
	// exactly what the closing observation is for.
	g.Commit("a.txt", "two")
	head := g.Commit("b.txt", "three")
	assertSilentSuccess(t, g.InProjectInput(g.hookInput("SessionEnd", nil), "hook", "session-end"))

	got := g.observations()
	if len(got) != 2 {
		t.Fatalf("spool holds %d observations, want one per session hook", len(got))
	}
	closing := got[1]
	if closing.Trigger != envelope.TriggerSessionEnd || closing.Head != head {
		t.Errorf("closing observation = %s at %s, want session_end at %s", closing.Trigger, closing.Head, head)
	}
	if closing.Base == nil || *closing.Base != got[0].Head {
		t.Errorf("base = %v, want the commit the session opened at (%s)", closing.Base, got[0].Head)
	}
	// The base is neither HEAD nor its first parent: the span covers
	// more than the last commit.
	if closing.Parent == nil || *closing.Base == *closing.Parent || *closing.Base == closing.Head {
		t.Errorf("observation = %+v, want a base of its own", closing)
	}
	changed := map[string]string{}
	for _, c := range closing.Changed {
		changed[c.Path] = c.Status
	}
	if changed["a.txt"] != "M" || changed["b.txt"] != "A" || closing.ChangedCount != 2 {
		t.Errorf("changed = %+v, want both files of the span", closing.Changed)
	}
}

func TestGitSnapshot_SessionEndObservesNothingChangedWhenHeadHasNotMoved(t *testing.T) {
	g := newGitEnv(t)
	g.Commit("a.txt", "one")
	assertSilentSuccess(t, g.InProjectInput(g.hookInput("SessionStart", nil), "hook", "ensure-proxy"))
	assertSilentSuccess(t, g.InProjectInput(g.hookInput("SessionEnd", nil), "hook", "session-end"))

	got := g.observations()
	if len(got) != 2 {
		t.Fatalf("spool holds %d observations, want one per session hook", len(got))
	}
	if got[1].Base != nil || len(got[1].Changed) != 0 {
		t.Errorf("closing observation = %+v, want nothing compared against an unmoved head", got[1])
	}
}

func TestGitSnapshot_AfterAToolUse(t *testing.T) {
	tests := []struct {
		name        string
		commits     bool
		extra       map[string]any
		wantTrigger string
	}{
		{
			name:        "a host that states the tool made a commit",
			commits:     true,
			extra:       toolUse("git commit -m x", true),
			wantTrigger: envelope.TriggerGitOperation,
		},
		{
			name:        "a host that states nothing beside a command that commits",
			commits:     true,
			extra:       toolUse("git commit -m x", false),
			wantTrigger: envelope.TriggerCommandMatch,
		},
		{
			name:  "a command that commits nothing",
			extra: toolUse("git status", false),
		},
		{
			name:  "a command that names a commit but moved no head",
			extra: toolUse("git commit --help", false),
		},
		{
			name:  "no tool members at all",
			extra: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGitEnv(t)
			root := g.Commit("a.txt", "one")
			// The session is opened first, so the degraded path has a
			// commit to find the head moved away from.
			assertSilentSuccess(t, g.InProjectInput(g.hookInput("SessionStart", nil), "hook", "ensure-proxy"))
			before := len(g.observations())
			if tt.commits {
				g.Commit("a.txt", "two")
			}

			assertSilentSuccess(t, g.InProjectInput(g.hookInput("PostToolUse", tt.extra), "hook", "git-snapshot"))

			got := g.observations()
			if tt.wantTrigger == "" {
				if len(got) != before {
					t.Fatalf("spool holds %d observations, want the %d it held before", len(got), before)
				}
				return
			}
			if len(got) != before+1 {
				t.Fatalf("spool holds %d observations, want one more than the %d before", len(got), before)
			}
			snap := got[len(got)-1]
			if snap.HookEvent != "PostToolUse" || snap.Trigger != tt.wantTrigger {
				t.Errorf("observation = %s/%s, want PostToolUse/%s", snap.HookEvent, snap.Trigger, tt.wantTrigger)
			}
			// A commit is compared against its own first parent.
			if snap.Base == nil || snap.Parent == nil || *snap.Base != *snap.Parent || *snap.Base != root {
				t.Errorf("base/parent = %v/%v, want both the commit before it (%s)", snap.Base, snap.Parent, root)
			}
			if len(snap.Changed) != 1 || snap.Changed[0].Path != "a.txt" || snap.Changed[0].Status != "M" {
				t.Errorf("changed = %+v, want the one file the commit touched", snap.Changed)
			}
		})
	}
}

func TestGitSnapshot_ObservesNothing(t *testing.T) {
	tests := []struct {
		name  string
		setup func(g *gitEnv)
		event string
	}{
		{
			name:  "a repository with no commit yet",
			setup: func(g *gitEnv) {},
			event: "SessionStart",
		},
		{
			name: "a device-wide pause",
			setup: func(g *gitEnv) {
				g.Commit("a.txt", "one")
				g.Sandbox().Pause(proxytest.PauseSignedOut)
			},
			event: "SessionStart",
		},
		{
			name:  "an event this client observes on no schedule of its own",
			setup: func(g *gitEnv) { g.Commit("a.txt", "one") },
			event: "UserPromptSubmit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGitEnv(t)
			tt.setup(g)
			assertSilentSuccess(t, g.InProjectInput(g.hookInput(tt.event, nil), "hook", "ensure-proxy"))
			if got := g.observations(); len(got) != 0 {
				t.Errorf("spool holds %+v, want nothing observed", got)
			}
		})
	}
}

func TestGitSnapshot_AProjectThatWasNeverEnabledIsNeverObserved(t *testing.T) {
	g := newUnenabledGitEnv(t)
	g.Commit("a.txt", "one")

	assertSilentSuccess(t, g.InProjectInput(g.hookInput("SessionStart", nil), "hook", "ensure-proxy"))
	if got := g.observations(); len(got) != 0 {
		t.Errorf("spool holds %+v for a project that was never enabled", got)
	}
}

func TestGitSnapshot_ARepositoryOutsideTheProjectIsNeverObserved(t *testing.T) {
	g := newGitEnv(t)
	g.Commit("a.txt", "one")
	outside := t.TempDir()

	input := g.hookInput("SessionStart", map[string]any{"cwd": outside})
	assertSilentSuccess(t, g.InProjectInput(input, "hook", "ensure-proxy"))
	if got := g.observations(); len(got) != 0 {
		t.Errorf("spool holds %+v for a directory outside the enabled project", got)
	}
}

func TestGitSnapshot_StatesWhereInsideTheProjectItRan(t *testing.T) {
	g := newGitEnv(t)
	g.Commit("apps/api/a.txt", "one")
	subdir := filepath.Join(g.Project(), "apps", "api")

	input := g.hookInput("SessionStart", map[string]any{"cwd": subdir})
	assertSilentSuccess(t, g.InProjectInput(input, "hook", "ensure-proxy"))

	snap := g.only()
	if snap.Capture.ProjectSubpath != "apps/api" {
		t.Errorf("project_subpath = %q, want where inside the project the session ran", snap.Capture.ProjectSubpath)
	}
	if strings.Contains(string(mustJSON(t, snap)), g.Project()) {
		t.Error("the record carries the absolute directory the session ran in")
	}
}

func TestGitSnapshot_TheSameObservationStoredTwiceIsOneRecord(t *testing.T) {
	g := newGitEnv(t)
	g.Commit("a.txt", "one")
	g.At(time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC))

	input := g.hookInput("SessionStart", nil)
	assertSilentSuccess(t, g.InProjectInput(input, "hook", "ensure-proxy"))
	assertSilentSuccess(t, g.InProjectInput(input, "hook", "ensure-proxy"))

	if got := g.observations(); len(got) != 1 {
		t.Errorf("spool holds %d observations of one moment, want one", len(got))
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
