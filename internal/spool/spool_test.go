package spool_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/spool"
)

var noon = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// indexName restates the documented on-disk layout. The spool's own
// tests are where that layout is pinned.
const indexName = "index.jsonl"

func TestWriteStoresEnvelopeUnderDayDirectory(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"schema_version":"1"}`)
	if err := s.Write(spool.Entry{RequestID: "msg_01", SessionKey: "sess-a", Timestamp: noon}, data); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "20260801", "msg_01.json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("rawcall file: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("stored bytes = %q, want %q", got, data)
	}

	if runtime.GOOS != "windows" {
		fileInfo, _ := os.Stat(path)
		if perm := fileInfo.Mode().Perm(); perm != 0o600 {
			t.Errorf("rawcall file mode = %o, want 600", perm)
		}
		dirInfo, _ := os.Stat(filepath.Join(dir, "20260801"))
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Errorf("day directory mode = %o, want 700", perm)
		}
	}

	entries, _ := os.ReadDir(filepath.Join(dir, "20260801"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestWriteAppendsSidecarIndexLines(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(spool.Entry{RequestID: "msg_01", SessionKey: "sess-a", Timestamp: noon}, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(spool.Entry{RequestID: "msg_02", Timestamp: noon}, []byte(`{"b":2}`)); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(filepath.Join(dir, "20260801", indexName))
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	defer f.Close()
	type line struct {
		RequestID  string `json:"request_id"`
		SessionKey string `json:"session_key"`
		Timestamp  string `json:"timestamp"`
		Size       int64  `json:"size"`
	}
	var lines []line
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("index line %q: %v", sc.Text(), err)
		}
		lines = append(lines, l)
	}
	if len(lines) != 2 {
		t.Fatalf("index has %d lines, want 2", len(lines))
	}
	want := line{RequestID: "msg_01", SessionKey: "sess-a", Timestamp: "2026-08-01T12:00:00Z", Size: 7}
	if lines[0] != want {
		t.Errorf("first index line = %+v, want %+v", lines[0], want)
	}
	if lines[1].RequestID != "msg_02" || lines[1].SessionKey != "" {
		t.Errorf("second index line = %+v", lines[1])
	}
}

func TestQuotaStopsWritesWithoutEvicting(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 200)
	if err != nil {
		t.Fatal(err)
	}
	first := []byte(strings.Repeat("a", 60))
	if err := s.Write(spool.Entry{RequestID: "msg_01", Timestamp: noon}, first); err != nil {
		t.Fatal(err)
	}
	err = s.Write(spool.Entry{RequestID: "msg_02", Timestamp: noon}, []byte(strings.Repeat("b", 60)))
	if !errors.Is(err, spool.ErrQuotaExceeded) {
		t.Fatalf("second write error = %v, want ErrQuotaExceeded", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "20260801", "msg_01.json")); err != nil {
		t.Errorf("existing rawcall evicted after quota hit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "20260801", "msg_02.json")); !os.IsNotExist(err) {
		t.Errorf("refused write left a file behind: %v", err)
	}
}

func TestOpenRecomputesUsageFromDisk(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(spool.Entry{RequestID: "msg_01", Timestamp: noon}, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	usage := s.Usage()
	if usage == 0 {
		t.Fatal("usage not tracked after write")
	}

	reopened, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Usage(); got != usage {
		t.Errorf("reopened usage = %d, want %d", got, usage)
	}
}

func TestOverwritingSameRequestIDDoesNotInflateUsage(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	entry := spool.Entry{RequestID: "msg_01", Timestamp: noon}
	if err := s.Write(entry, []byte(strings.Repeat("a", 40))); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(entry, []byte(strings.Repeat("b", 40))); err != nil {
		t.Fatal(err)
	}

	reopened, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.Usage(), reopened.Usage(); got != want {
		t.Errorf("tracked usage %d diverged from on-disk usage %d", got, want)
	}
}

func TestWriteRefusesRequestIDsThatCouldEscapeTheDayDirectory(t *testing.T) {
	s, err := spool.Create(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"msg_01AB", "req-1.2", "local-abcdef"} {
		if err := s.Write(spool.Entry{RequestID: id, Timestamp: noon}, []byte("{}")); err != nil {
			t.Errorf("Write(%q) = %v, want accepted", id, err)
		}
	}
	for _, id := range []string{"", "../escape", "a/b", ".hidden"} {
		if err := s.Write(spool.Entry{RequestID: id, Timestamp: noon}, []byte("{}")); err == nil {
			t.Errorf("Write(%q) = nil, want refused", id)
		}
	}
}

func TestCreateReportsConfigurationAndFailures(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Quota() != spool.DefaultQuota {
		t.Errorf("Quota = %d, want the default", s.Quota())
	}

	custom, err := spool.Create(filepath.Join(dir, "sub"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	if custom.Quota() != 4096 {
		t.Errorf("custom quota = %d", custom.Quota())
	}

	blocked := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Create(filepath.Join(blocked, "rawcalls"), 0); err == nil {
		t.Error("Create under a plain file succeeded")
	}
}

func TestOpenCreatesNothingAndReadsBackAnUnwrittenSpool(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rawcalls")
	s, err := spool.Open(dir, 0)
	if err != nil {
		t.Fatalf("Open on an unwritten spool = %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("Open created the spool directory")
	}
	if s.Usage() != 0 {
		t.Errorf("Usage = %d, want 0", s.Usage())
	}
	if err := s.Each(func(spool.Rawcall) error {
		t.Error("an unwritten spool yielded a rawcall")
		return nil
	}); err != nil {
		t.Errorf("Each = %v", err)
	}
	deleted, err := s.DeleteWhere(func(spool.Rawcall) bool { return true })
	if err != nil || deleted != 0 {
		t.Errorf("DeleteWhere = %d, %v, want 0, nil", deleted, err)
	}
	if err := s.Write(spool.Entry{RequestID: "msg_01", Timestamp: noon}, []byte("{}")); err != nil {
		t.Errorf("Write after a non-creating Open = %v", err)
	}
}

func TestWriteSurfacesFilesystemFailures(t *testing.T) {
	t.Run("day directory blocked by a file", func(t *testing.T) {
		dir := t.TempDir()
		s, err := spool.Create(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "20260801"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.Write(spool.Entry{RequestID: "msg_01", Timestamp: noon}, []byte("{}")); err == nil {
			t.Error("Write succeeded with the day directory blocked")
		}
	})
	t.Run("index blocked by a directory", func(t *testing.T) {
		dir := t.TempDir()
		s, err := spool.Create(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "20260801", indexName), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := s.Write(spool.Entry{RequestID: "msg_01", Timestamp: noon}, []byte("{}")); err == nil {
			t.Error("Write succeeded with the index unwritable")
		}
	})
}

func TestWriteRejectsUnsafeRequestIDs(t *testing.T) {
	s, err := spool.Create(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../escape", "a/b", ".hidden", "-dash-first", "id with space"} {
		if err := s.Write(spool.Entry{RequestID: id, Timestamp: noon}, []byte("{}")); err == nil {
			t.Errorf("request id %q accepted", id)
		}
	}
}

func TestSetQuotaGovernsSubsequentWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 200)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(spool.Entry{RequestID: "msg_01", Timestamp: noon}, []byte(strings.Repeat("a", 60))); err != nil {
		t.Fatal(err)
	}

	s.SetQuota(s.Usage())
	err = s.Write(spool.Entry{RequestID: "msg_02", Timestamp: noon}, []byte("b"))
	if !errors.Is(err, spool.ErrQuotaExceeded) {
		t.Fatalf("write after shrinking the quota = %v, want ErrQuotaExceeded", err)
	}

	s.SetQuota(1 << 20)
	if err := s.Write(spool.Entry{RequestID: "msg_02", Timestamp: noon}, []byte("b")); err != nil {
		t.Fatalf("write after raising the quota = %v", err)
	}
	if got := s.Quota(); got != 1<<20 {
		t.Errorf("Quota = %d, want %d", got, 1<<20)
	}

	s.SetQuota(0)
	if got := s.Quota(); got != 1<<20 {
		t.Errorf("Quota after SetQuota(0) = %d, want unchanged %d", got, 1<<20)
	}
}
