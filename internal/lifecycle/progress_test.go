package lifecycle_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

// aSessionWithAnAgentFile puts a session's main file and one agent
// file beside it, and returns the main file's path.
func (e *env) aSessionWithAnAgentFile() string {
	e.t.Helper()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", "")
	e.putSessionFile("-work-sample/0f1e2d3c/subagents/agent-x.jsonl", "")
	return main
}

func TestSessionProgressedMarksTheSessionFileHot(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	main := e.aSessionWithAnAgentFile()

	e.machine().SessionProgressed(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "Stop"})

	files := e.registeredFiles(e.canonicalRoot())
	if len(files) != 2 || files[0].Path != main {
		t.Fatalf("registry = %+v, want the session file and the agent file beside it", files)
	}
	for _, f := range files {
		if f.PID != os.Getppid() || f.LastEvent == "" {
			t.Errorf("%s = %+v, want hot, carrying the session's process", filepath.Base(f.Path), f)
		}
	}
}

func TestSessionEndedMarksTheSessionFileCold(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	main := e.aSessionWithAnAgentFile()
	e.machine().SessionProgressed(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "Stop"})

	e.machine().SessionEnded(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "SessionEnd"})

	files := e.registeredFiles(e.canonicalRoot())
	if len(files) != 2 {
		t.Fatalf("registry = %+v, want the session file and the agent file beside it", files)
	}
	for _, f := range files {
		if f.PID != 0 || f.LastEvent != "" {
			t.Errorf("%s after the session ended = %+v, want cold", filepath.Base(f.Path), f)
		}
	}
}

// putWorkflowAgent puts the lines and the metadata of one agent of a
// workflow run under the session 0f1e2d3c, and returns the path of each
// relative to the session's directory, in slash form.
func (e *env) putWorkflowAgent(run, agent string) []string {
	e.t.Helper()
	dir := "subagents/workflows/" + run + "/agent-" + agent
	e.putSessionFile("-work-sample/0f1e2d3c/"+dir+".jsonl",
		`{"type":"assistant","agentId":"`+agent+`","message":{"id":"msg-`+run+`-`+agent+`","role":"assistant"}}`+"\n")
	e.putSessionFile("-work-sample/0f1e2d3c/"+dir+".meta.json",
		`{"agentType":"workflow-subagent","workflowPhase":"p"}`)
	return []string{dir + ".jsonl", dir + ".meta.json"}
}

// storedFileLabels reads the place in its session that every stored
// segment and snapshot names, and the session it names.
func (e *env) storedFileLabels() map[string]string {
	e.t.Helper()
	labels := map[string]string{}
	for _, r := range e.storedRecords() {
		var rec struct {
			SessionID string `json:"session_id"`
			File      string `json:"file"`
		}
		if err := json.Unmarshal(r.Raw, &rec); err != nil {
			e.t.Fatalf("record %s: %v", r.ID, err)
		}
		labels[rec.File] = rec.SessionID
	}
	return labels
}

func TestSessionProgressedOnTheLastStopTakesEveryAgentFileOfAWorkflowRun(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", `{"type":"user","message":{"id":"msg-main"}}`+"\n")
	hook := lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "Stop"}
	e.machine().SessionProgressed(e.project, hook)
	want := slices.Concat(e.putWorkflowAgent("wf_1a2b-3c", "a1"), e.putWorkflowAgent("wf_1a2b-3c", "a2"))
	want = append(want, e.putWorkflowAgent("wf_1a2b-3c", "b1")...)

	e.machine().SessionProgressed(e.project, hook)
	e.machine().ReadSessionFiles(e.project, discardIO())

	if got := len(e.registeredFiles(e.canonicalRoot())); got != 1+len(want) {
		t.Errorf("registered %d files, want the main file and %d agent files", got, len(want))
	}
	labels := e.storedFileLabels()
	for _, file := range append([]string{""}, want...) {
		if session, ok := labels[file]; !ok || session != "0f1e2d3c" {
			t.Errorf("stored records = %v, want one of session 0f1e2d3c for %q", labels, file)
		}
	}
	if len(labels) != 1+len(want) {
		t.Errorf("stored records name %d files, want %d: %v", len(labels), 1+len(want), labels)
	}
}

func TestSessionProgressedTakesDozensOfAgentsAcrossSeveralRuns(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", `{"type":"user","message":{"id":"msg-main"}}`+"\n")
	var want []string
	for run := range 3 {
		for agent := range 12 {
			want = append(want, e.putWorkflowAgent(fmt.Sprintf("wf_run%d", run), fmt.Sprintf("a%02d", agent))...)
		}
	}

	e.machine().SessionProgressed(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "Stop"})
	e.machine().ReadSessionFiles(e.project, discardIO())

	if got := len(e.registeredFiles(e.canonicalRoot())); got != 1+len(want) {
		t.Errorf("registered %d files, want the main file and %d agent files", got, len(want))
	}
	labels := e.storedFileLabels()
	for _, file := range want {
		if _, ok := labels[file]; !ok {
			t.Errorf("no stored record for %q", file)
		}
	}
}

func TestSessionProgressedStoresEveryMessageOfAWorkflowRunAndNotItsJournal(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	assistant := func(id string) string {
		return `{"type":"assistant","requestId":"req-` + id + `","message":{"id":"` + id + `","role":"assistant","content":[{"type":"text","text":"ok"}]}}` + "\n"
	}
	want := map[string]bool{}
	var mainLines strings.Builder
	mainLines.WriteString(`{"type":"user","message":{"role":"user","content":"run it"}}` + "\n")
	for _, id := range []string{"msg-main-1", "msg-main-2"} {
		mainLines.WriteString(assistant(id))
		want[id] = true
	}
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", mainLines.String())
	run := "-work-sample/0f1e2d3c/subagents/workflows/wf_7c-1/"
	wantFiles := map[string]bool{"": true}
	for _, agent := range []string{"a1", "a2", "a3"} {
		var lines strings.Builder
		lines.WriteString(`{"type":"user","isSidechain":true,"agentId":"` + agent + `","message":{"role":"user","content":"phase"}}` + "\n")
		for n := range 3 {
			id := fmt.Sprintf("msg-%s-%d", agent, n)
			lines.WriteString(assistant(id))
			want[id] = true
		}
		e.putSessionFile(run+"agent-"+agent+".jsonl", lines.String())
		e.putSessionFile(run+"agent-"+agent+".meta.json", `{"agentType":"workflow-subagent","workflowPhase":"review","spawnDepth":1}`)
		wantFiles["subagents/workflows/wf_7c-1/agent-"+agent+".jsonl"] = true
		wantFiles["subagents/workflows/wf_7c-1/agent-"+agent+".meta.json"] = true
	}
	e.putSessionFile(run+"journal.jsonl", `{"type":"result","agentId":"a1","result":{"id":"msg-journal"}}`+"\n")

	e.machine().SessionProgressed(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "Stop"})
	e.machine().ReadSessionFiles(e.project, discardIO())

	got := map[string]bool{}
	gotFiles := map[string]bool{}
	for _, r := range e.storedRecords() {
		var rec struct {
			SessionID string `json:"session_id"`
			File      string `json:"file"`
			Lines     string `json:"lines"`
		}
		if err := json.Unmarshal(r.Raw, &rec); err != nil {
			t.Fatalf("record %s: %v", r.ID, err)
		}
		if rec.SessionID != "0f1e2d3c" {
			t.Errorf("record of %q names session %q, want 0f1e2d3c", rec.File, rec.SessionID)
		}
		gotFiles[rec.File] = true
		for line := range strings.SplitSeq(strings.TrimSuffix(rec.Lines, "\n"), "\n") {
			var row struct {
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			}
			if line != "" && json.Unmarshal([]byte(line), &row) == nil && row.Message.ID != "" {
				got[row.Message.ID] = true
			}
		}
	}
	if !maps.Equal(got, want) {
		t.Errorf("message ids stored = %v, want %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	if !maps.Equal(gotFiles, wantFiles) {
		t.Errorf("files stored = %v, want %v", slices.Sorted(maps.Keys(gotFiles)), slices.Sorted(maps.Keys(wantFiles)))
	}
	for _, path := range e.registeredPaths(e.canonicalRoot()) {
		if filepath.Base(path) == "journal.jsonl" {
			t.Errorf("registry holds %s, want the run's journal left alone", path)
		}
	}
}
