package lifecycle_test

import (
	"os"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

func TestSessionProgressedMarksTheSessionFileHot(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", "")

	e.machine().SessionProgressed(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "Stop"})

	files := e.registeredFiles(e.canonicalRoot())
	if len(files) != 1 || files[0].Path != main {
		t.Fatalf("registry = %+v, want the session file", files)
	}
	if files[0].PID != os.Getppid() || files[0].LastEvent == "" {
		t.Errorf("file = %+v, want hot, carrying the session's process", files[0])
	}
}

func TestSessionEndedMarksTheSessionFileCold(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", "")
	e.machine().SessionProgressed(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "Stop"})

	e.machine().SessionEnded(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "SessionEnd"})

	files := e.registeredFiles(e.canonicalRoot())
	if len(files) != 1 || files[0].PID != 0 || files[0].LastEvent != "" {
		t.Errorf("file after the session ended = %+v, want cold", files)
	}
}
