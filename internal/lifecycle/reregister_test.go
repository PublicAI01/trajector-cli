package lifecycle_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

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

func TestEnableAfterASessionLeftTheProjectLeavesItRetiredAndSendsItsSegmentOnce(t *testing.T) {
	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", ackBatch(nil))
	servedProxy(t, e)
	root := e.canonicalRoot()
	path := e.putSessionFile(discover.Encode(root)+"/0f1e2d3c.jsonl",
		`{"type":"user","message":{"id":"m1"}}`+"\n")

	e.enable(proxytest.WithProxy)
	m := e.machine()
	m.ReadSessionFiles(e.project, discardIO())
	if got := e.storedRecords(); len(got) != 1 {
		t.Fatalf("records after the first reading run = %d, want the one segment", len(got))
	}
	if err := m.Upload(true, e.io()); err != nil {
		t.Fatal(err)
	}
	if got := e.storedRecords(); len(got) != 0 {
		t.Fatalf("records after the acknowledged upload = %d, want the spool emptied", len(got))
	}

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
