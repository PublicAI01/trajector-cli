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

func TestEnableAfterASessionLeftTheProjectRegistersTheFileAgainAndSendsItsSegmentTwice(t *testing.T) {
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
	files := e.registeredFiles(root)
	if len(files) != 1 || files[0].Path != path {
		t.Fatalf("registry after the second enable = %+v, want the retired file registered again", files)
	}
	if f := files[0]; f.Offset != 0 || f.NextSegment != 0 || f.Size != 0 || f.Inode != 0 || len(f.MessageIDs) != 0 {
		t.Fatalf("cursor of the file registered again = %+v, want a zero cursor", f)
	}

	m.ReadSessionFiles(e.project, discardIO())
	if got := e.storedRecords(); len(got) != 1 {
		t.Fatalf("records after reading the file registered again = %d, want the segment stored once more", len(got))
	}
	if err := m.Upload(true, e.io()); err != nil {
		t.Fatal(err)
	}

	sent := sentRecordIDs(t, e)
	if len(sent) != 2 {
		t.Fatalf("uploaded batches = %+v, want two", sent)
	}
	if len(sent[0]) != 1 || len(sent[1]) != 1 || sent[0][0] != sent[1][0] {
		t.Fatalf("uploaded batches carried %+v, want one record id, the same one, in both", sent)
	}
}
