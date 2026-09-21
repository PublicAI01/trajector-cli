package lifecycle_test

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// appendLine adds one line to a session file, as Claude Code adds one
// while a session runs.
func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// sentRecordIDs lists the record ids each uploaded batch carried, in
// the order the service received them.
func sentRecordIDs(t *testing.T, e *env) [][]string {
	t.Helper()
	var sent [][]string
	for _, r := range e.service.Requests() {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL, "/v1/batches") {
			continue
		}
		b, err := parseBatch(r)
		if err != nil {
			t.Fatal(err)
		}
		sent = append(sent, b.RecordIDs)
	}
	return sent
}

// waitForTheSegmentToBeSent waits until the service holds the one
// batch and the spool is empty again. enable asks the resident process
// to read the file it just registered, and the resident process is
// the flusher, so the segment is read, sent and acknowledged with no
// reading run and no upload of this test's own.
func waitForTheSegmentToBeSent(t *testing.T, e *env) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		sent := sentRecordIDs(t, e)
		held := e.storedRecords()
		if len(sent) == 1 && len(sent[0]) == 1 && len(held) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("batches = %+v and %d record(s) still in the spool, want the one segment sent once and the spool emptied", sent, len(held))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEnableAfterASessionLeftTheProjectLeavesItRetiredAndSendsItsSegmentOnce(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", ackBatch(nil))
	servedProxy(t, e)
	root := e.canonicalRoot()
	path := e.putSessionFile(discover.Encode(root)+"/0f1e2d3c.jsonl",
		`{"type":"user","message":{"id":"m1"}}`+"\n")

	e.enable(proxytest.WithProxy)
	m := e.machine()
	waitForTheSegmentToBeSent(t, e)

	appendLine(t, path, `{"type":"relocated","relocatedCwd":"/elsewhere/entirely"}`)
	m.ReadSessionFiles(e.project, discardIO())
	if got := e.registeredFiles(root); len(got) != 0 {
		t.Fatalf("registry after the session left the project = %+v, want the entry retired", got)
	}

	e.enable(proxytest.WithProxy)
	if got := e.registeredFiles(root); len(got) != 0 {
		t.Fatalf("registry after the second enable = %+v, want the retired file left alone", got)
	}

	m.ReadSessionFiles(e.project, discardIO())
	if got := e.storedRecords(); len(got) != 0 {
		t.Fatalf("records after the second enable = %d, want nothing read of the retired file", len(got))
	}
	if err := m.Upload(true, e.io()); err != nil {
		t.Fatal(err)
	}

	sent := sentRecordIDs(t, e)
	if len(sent) != 1 || len(sent[0]) != 1 {
		t.Fatalf("uploaded batches = %+v, want the one batch with the one record", sent)
	}
}
