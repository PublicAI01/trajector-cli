package lifecycle_test

import (
	"os"
	"path/filepath"
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
