package proxyserve_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// earlierSessionFile writes one session file with content, for a
// registry an earlier build left to hold.
func (e *env) earlierSessionFile(name, content string) string {
	e.t.Helper()
	path := filepath.Join(e.t.TempDir(), "-work-sample", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return path
}

func TestTheResidentProcessMigratesARegistryAnEarlierBuildLeftAndReadsIt(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	hash := e.enabledProject()
	hot := e.earlierSessionFile("hot.jsonl", sessionLine)
	named := e.earlierSessionFile("named.jsonl", sessionLine)
	e.sandbox.LeaveEarlierRegistry(hash, proxytest.EarlierRegistry{Files: []proxytest.EarlierEntry{
		{Path: hot, LastEvent: time.Now().UTC().Format(time.RFC3339), PID: os.Getpid()},
		{Path: named},
	}})

	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()
	defer e.stopServed(served)

	read := func() bool {
		files, err := e.registry().Files(hash)
		return err == nil && len(files) == 2 && slices.ContainsFunc(files, func(f follow.File) bool { return f.Path == hot && f.Offset > 0 })
	}
	if !eventually(t, 5*time.Second, read) {
		t.Fatalf("registry version %d, want the registry migrated and the hot file read by the sweep on the way in", e.sandbox.RegistryVersion(hash))
	}
	if status := e.progress(apiproxy.Progress{ProjectIDHash: hash, Path: named}); status != http.StatusAccepted {
		t.Fatalf("progress = %d, want 202", status)
	}
	if f := e.entry(hash, named); f.Offset == 0 {
		t.Errorf("named file = %+v, want read on the report", f)
	}
}

func TestTheResidentProcessLeavesAProjectAnotherProcessIsMigrating(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	hash := e.enabledProject()
	named := e.earlierSessionFile("named.jsonl", sessionLine)
	e.sandbox.LeaveEarlierRegistry(hash, proxytest.EarlierRegistry{Files: []proxytest.EarlierEntry{{Path: named}}})
	e.sandbox.MigratingElsewhere(hash)

	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()
	defer e.stopServed(served)

	if status := e.progress(apiproxy.Progress{ProjectIDHash: hash, Path: named}); status != http.StatusNotFound {
		t.Errorf("progress = %d, want 404: the project is not read until its migration is done", status)
	}
	if v := e.sandbox.RegistryVersion(hash); v != 1 {
		t.Errorf("registry version = %d, want it left to the other process at 1", v)
	}
	if n := len(e.sandbox.Records()); n != 0 {
		t.Errorf("records = %d, want none", n)
	}
}
