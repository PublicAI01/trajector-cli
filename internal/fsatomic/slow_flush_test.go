package fsatomic_test

// These tests drive a proxy, and live here because only this package's
// tests can hold a flush open.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

func holdFlushOf(t *testing.T, requestID string) (held <-chan string, release func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not remove a file that is open, so no sweep takes a temp while its flush is held")
	}
	began := make(chan string, 1)
	gate := make(chan struct{})
	var once sync.Once
	fsatomic.SwapSyncFile(t, func(f *os.File) error {
		if strings.HasPrefix(filepath.Base(f.Name()), requestID+".json.tmp-") {
			once.Do(func() {
				began <- f.Name()
				<-gate
			})
		}
		return f.Sync()
	})
	return began, sync.OnceFunc(func() { close(gate) })
}

func heldTemp(t *testing.T, held <-chan string) string {
	t.Helper()
	select {
	case path := <-held:
		return path
	case <-time.After(10 * time.Second):
		t.Fatal("the capture never reached its flush")
		return ""
	}
}

func ageBeyondTheStaleBound(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func twoProjects(upstream, firstRevokedAt string) string {
	first := map[string]string{
		"project_id_hash": "hash-p1",
		"root_path":       "/home/dev/first",
		"upstream":        upstream,
		"granted_at":      "2026-08-01T00:00:00Z",
	}
	if firstRevokedAt != "" {
		first["revoked_at"] = firstRevokedAt
	}
	table, _ := json.Marshal(map[string]any{
		"projects": map[string]any{
			"tok1": first,
			"tok2": map[string]string{
				"project_id_hash": "hash-p2",
				"root_path":       "/home/dev/second",
				"upstream":        upstream,
				"granted_at":      "2026-08-01T00:00:00Z",
			},
		},
	})
	return string(table)
}

func exchange(t *testing.T, e *proxytest.Env, token, requestID string) {
	t.Helper()
	e.Upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"` + requestID + `"}`)})
	if resp := e.Post("/t/"+token+"/v1/messages", `{"m":1}`, nil); resp.StatusCode != 200 {
		t.Fatalf("forwarding %s: status %d", requestID, resp.StatusCode)
	}
}

func TestCaptureIsRecordedWhenAnotherHandleOpensTheSpoolDuringItsSlowFlush(t *testing.T) {
	held, release := holdFlushOf(t, "msg_slow")
	e := proxytest.New(t)
	t.Cleanup(release)
	e.WriteTable(twoProjects(e.Upstream.URL(), ""))

	exchange(t, e, "tok1", "msg_slow")
	ageBeyondTheStaleBound(t, heldTemp(t, held))
	if _, err := spool.Open(e.Layout().SpoolDir(), 0); err != nil {
		t.Fatal(err)
	}
	release()

	if stored := e.WaitRawcalls(1); stored[0].RequestID != "msg_slow" {
		t.Errorf("rawcalls = %+v, want the capture whose flush was slow", stored)
	}
	if dropped := e.Dropped().Snapshot(); len(dropped.Reasons) != 0 {
		t.Errorf("dropped captures = %+v, want none", dropped.Reasons)
	}
}

func TestCaptureIsNotWrittenAgainOnceItsProjectWasDisabledDuringItsSlowFlush(t *testing.T) {
	held, release := holdFlushOf(t, "msg_slow")
	e := proxytest.New(t)
	t.Cleanup(release)
	e.WriteTable(twoProjects(e.Upstream.URL(), ""))

	exchange(t, e, "tok1", "msg_slow")
	ageBeyondTheStaleBound(t, heldTemp(t, held))
	e.WriteTable(twoProjects(e.Upstream.URL(), "2026-08-02T00:00:00Z"))
	disabling, err := spool.Open(e.Layout().SpoolDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabling.DeleteProject("hash-p1"); err != nil {
		t.Fatal(err)
	}
	release()

	// Captures are written one at a time and in order, so the second
	// being stored means the first is settled.
	exchange(t, e, "tok2", "msg_after")
	if stored := e.WaitRawcalls(1); len(stored) != 1 || stored[0].RequestID != "msg_after" {
		t.Errorf("rawcalls = %+v, want only the enabled project's capture", stored)
	}
	if dropped := e.Dropped().Snapshot(); len(dropped.Reasons) != 0 {
		t.Errorf("dropped captures = %+v, want none: a withdrawn capture is not a failure", dropped.Reasons)
	}
}
