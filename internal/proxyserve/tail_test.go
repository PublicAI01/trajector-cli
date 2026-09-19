package proxyserve_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/proxyserve"
)

const sessionLine = `{"type":"assistant","message":{"id":"m1"}}` + "\n"

// enabledProject grants one project and returns its root and hash.
func (e *env) enabledProject() (root, hash string) {
	e.t.Helper()
	root = e.t.TempDir()
	hash = proxytest.ProjectIDHash(root)
	e.sandbox.GrantProject(proxytest.Grant{
		Token:         "tok-proj",
		ProjectIDHash: hash,
		RootPath:      root,
		Upstream:      "https://api.anthropic.com",
	})
	return root, hash
}

// sessionFile writes one session file with content and registers it
// for the project.
func (e *env) sessionFile(hash, name, content string) string {
	e.t.Helper()
	path := filepath.Join(e.t.TempDir(), "-work-sample", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.sandbox.RegisterSessionFile(hash, path, "")
	return path
}

func (e *env) registry() *follow.Registry {
	return follow.Open(e.assembly.Layout.FollowDir())
}

// warm marks the session path belongs to hot, carrying this process,
// as a session hook does before it reports progress.
func (e *env) warm(hash, path string) {
	e.t.Helper()
	if err := e.registry().Warm(hash, path, os.Getpid(), time.Now()); err != nil {
		e.t.Fatal(err)
	}
}

// cool marks the session path belongs to cold, as a session hook does
// before it reports the session's end.
func (e *env) cool(hash, path string) {
	e.t.Helper()
	if err := e.registry().Cool(hash, path); err != nil {
		e.t.Fatal(err)
	}
}

// entry is one registered file of a project, by path.
func (e *env) entry(hash, path string) follow.File {
	e.t.Helper()
	files, err := e.registry().Files(hash)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	e.t.Fatalf("%q is not registered", path)
	return follow.File{}
}

// progress posts one progress report with the admin token and returns
// the status.
func (e *env) progress(ev apiproxy.Progress) int {
	e.t.Helper()
	body, err := json.Marshal(ev)
	if err != nil {
		e.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+e.assembly.Addr+apiproxy.ProgressPath, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	proxytest.Authorize(req, e.assembly.Layout)
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (e *env) stopServed(served <-chan error) {
	e.t.Helper()
	e.adminPost(apiproxy.DrainPath)
	e.waitExit(served, 10*time.Second)
}

// eventually polls until check holds or the deadline passes.
func eventually(t *testing.T, within time.Duration, check func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return check()
}

func TestProgressReportReadsTheOneFileItNames(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	_, hash := e.enabledProject()
	named := e.sessionFile(hash, "named.jsonl", sessionLine)
	other := e.sessionFile(hash, "other.jsonl", sessionLine)
	e.warm(hash, named)

	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()
	defer e.stopServed(served)

	if status := e.progress(apiproxy.Progress{ProjectIDHash: hash, Path: named}); status != http.StatusAccepted {
		t.Fatalf("progress = %d, want 202", status)
	}
	records := e.sandbox.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d, want the named file's one segment", len(records))
	}
	files, err := e.registry().Files(hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		switch f.Path {
		case named:
			if f.Offset == 0 {
				t.Errorf("named file = %+v, want read", f)
			}
		case other:
			if f.Offset != 0 || f.LastEvent != "" {
				t.Errorf("other file = %+v, want untouched", f)
			}
		}
	}
}

func TestProgressReportLeavesTheHeatTheHookWrote(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	_, hash := e.enabledProject()
	path := e.sessionFile(hash, "s.jsonl", sessionLine)
	e.warm(hash, path)
	before := e.entry(hash, path)

	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()
	defer e.stopServed(served)

	if status := e.progress(apiproxy.Progress{ProjectIDHash: hash, Path: path}); status != http.StatusAccepted {
		t.Fatalf("progress = %d, want 202", status)
	}
	after := e.entry(hash, path)
	if after.LastEvent != before.LastEvent || after.PID != before.PID {
		t.Errorf("file after the report = %+v, want the event time and the process the hook wrote: %+v", after, before)
	}
}

func TestProgressReportRefusesAFileThatIsNotRegistered(t *testing.T) {
	e := newEnv(t)
	_, hash := e.enabledProject()
	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()
	defer e.stopServed(served)

	if status := e.progress(apiproxy.Progress{ProjectIDHash: hash, Path: filepath.Join(t.TempDir(), "x.jsonl")}); status != http.StatusNotFound {
		t.Errorf("progress for an unregistered file = %d, want 404", status)
	}
	if status := e.progress(apiproxy.Progress{}); status != http.StatusBadRequest {
		t.Errorf("empty progress = %d, want 400", status)
	}
}

func TestProgressReportOfASessionEndReadsFlushesAndCools(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", ackBatch)
	_, hash := e.enabledProject()
	path := e.sessionFile(hash, "s.jsonl", sessionLine)
	e.warm(hash, path)

	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()
	defer e.stopServed(served)

	e.cool(hash, path)
	if status := e.progress(apiproxy.Progress{ProjectIDHash: hash, Path: path, End: true}); status != http.StatusAccepted {
		t.Fatalf("progress = %d, want 202", status)
	}
	if !eventually(t, 10*time.Second, func() bool { return len(e.service.Requests()) == 1 }) {
		t.Errorf("service saw %d requests, want the one flush a session's end asks for", len(e.service.Requests()))
	}
	if !eventually(t, 10*time.Second, func() bool { return len(e.sandbox.Records()) == 0 }) {
		t.Errorf("spool holds %d records after the end flush, want 0", len(e.sandbox.Records()))
	}
	files, err := e.registry().Files(hash)
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	if files[0].Offset == 0 || files[0].PID != 0 || files[0].LastEvent != "" {
		t.Errorf("file after the end = %+v, want read to its end and cold", files[0])
	}
}

func TestSweepReadsHotFilesAndNeverColdOnes(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.assembly.SweepInterval = 50 * time.Millisecond
	_, hash := e.enabledProject()
	hot := e.sessionFile(hash, "hot.jsonl", "")
	cold := e.sessionFile(hash, "cold.jsonl", "")
	if err := e.registry().Warm(hash, hot, 0, time.Now()); err != nil {
		t.Fatal(err)
	}

	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()
	defer e.stopServed(served)

	for _, path := range []string{hot, cold} {
		if err := os.WriteFile(path, []byte(sessionLine), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !eventually(t, 5*time.Second, func() bool { return len(e.sandbox.Records()) == 1 }) {
		t.Fatalf("records = %d, want the hot file's one segment read by the sweep", len(e.sandbox.Records()))
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(e.sandbox.Records()); n != 1 {
		t.Errorf("records = %d after more sweeps, want still 1: the cold file is never read", n)
	}
	files, err := e.registry().Files(hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == cold && (f.Offset != 0 || f.ReadAt != "") {
			t.Errorf("cold file = %+v, want never read", f)
		}
	}
}

func TestARunningSessionProcessStretchesTheIdleExit(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	_, hash := e.enabledProject()
	path := e.sessionFile(hash, "s.jsonl", "")
	e.warm(hash, path)

	const idle = 300 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		done <- proxyserve.Serve(t.Context(), e.assembly, idle, io.Discard, io.Discard)
	}()
	e.waitHealthy()

	select {
	case err := <-done:
		t.Fatalf("Serve returned %v within the plain idle timeout while a session process runs", err)
	case <-time.After(2 * idle):
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v, want a clean idle exit", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve never exited: a running session process must stretch the idle exit, not cancel it")
	}
}

func TestProgressReportReadsTheAgentFilesBesideTheNamedOne(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	_, hash := e.enabledProject()
	named := e.sessionFile(hash, "sess.jsonl", sessionLine)
	// A subagent's lines live beside the main file, under the session's
	// own directory; a hook registers them but never names them.
	agent := filepath.Join(filepath.Dir(named), "sess", follow.SubagentsDir, "agent-x.jsonl")
	if err := os.MkdirAll(filepath.Dir(agent), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent, []byte(sessionLine), 0o600); err != nil {
		t.Fatal(err)
	}
	e.sandbox.RegisterSessionFile(hash, agent, "")
	e.warm(hash, named)

	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()
	defer e.stopServed(served)

	if status := e.progress(apiproxy.Progress{ProjectIDHash: hash, Path: named}); status != http.StatusAccepted {
		t.Fatalf("progress = %d, want 202", status)
	}
	if records := e.sandbox.Records(); len(records) != 2 {
		t.Fatalf("records = %d, want the main file's segment and the agent file's", len(records))
	}
	files, err := e.registry().Files(hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Offset == 0 || f.LastEvent == "" {
			t.Errorf("%s = %+v, want read and hot", filepath.Base(f.Path), f)
		}
	}
}

func TestProgressReportAnswersBeforeTheFlushRuns(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	_, hash := e.enabledProject()
	path := e.sessionFile(hash, "s.jsonl", sessionLine)
	e.warm(hash, path)
	reached := make(chan struct{}, 1)
	held := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(held) }) }
	e.service.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-held
		return ackBatch(r)
	})

	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()
	defer e.stopServed(served)
	defer release()

	answered := make(chan int, 1)
	go func() {
		answered <- e.progress(apiproxy.Progress{ProjectIDHash: hash, Path: path, End: true})
	}()
	select {
	case status := <-answered:
		if status != http.StatusAccepted {
			t.Fatalf("progress = %d, want 202", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the progress report waited on the upload it asked for")
	}
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the flush a session's end asks for never reached the service")
	}
	release()
}

func TestSweepSendsTheRecordsItReadsAtOnce(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", ackBatch)
	e.assembly.SweepInterval = 50 * time.Millisecond
	_, hash := e.enabledProject()
	hot := e.sessionFile(hash, "hot.jsonl", "")
	if err := e.registry().Warm(hash, hot, 0, time.Now()); err != nil {
		t.Fatal(err)
	}

	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()
	defer e.stopServed(served)

	if err := os.WriteFile(hot, []byte(sessionLine), 0o600); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, 10*time.Second, func() bool { return len(e.service.Requests()) > 0 }) {
		t.Fatal("the service never saw what a sweep read, and no threshold of the records' own was near")
	}
	if !eventually(t, 10*time.Second, func() bool { return len(e.sandbox.Records()) == 0 }) {
		t.Errorf("spool holds %d records after the sweep's flush, want 0", len(e.sandbox.Records()))
	}
}
