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

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

var noon = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// indexName restates the documented on-disk layout. The spool's own
// tests are where that layout is pinned.
const indexName = "index.jsonl"

// storedRawcall builds one rawcall in stored form through the
// envelope's own writer, so these tests never spell the serialized
// layout themselves.
func storedRawcall(t *testing.T, id, sessionKey, projectHash string, at time.Time) envelope.Envelope {
	t.Helper()
	env, err := envelope.Record(envelope.Observation{
		ProjectIDHash:    projectHash,
		At:               at,
		Request:          []byte(`{"metadata":{"user_id":"` + sessionKey + `"}}`),
		RequestComplete:  true,
		Response:         []byte(`{"id":"` + id + `"}`),
		ResponseComplete: true,
		ContentType:      "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// parsedRawcall hand-writes a stored record and reads it back through
// Parse, for ids and timestamps the envelope's own writer would never
// emit.
func parsedRawcall(t *testing.T, id, timestamp string) envelope.Envelope {
	t.Helper()
	quoted, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"schema_version":"1","request_id":` + string(quoted) + `,"capture":{"timestamp":"` + timestamp + `"}}`
	env, err := envelope.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestWriteStoresEnvelopeUnderDayDirectory(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	env := storedRawcall(t, "msg_01", "sess-a", "hash-a", noon)
	data := env.Bytes()
	if err := s.Write(env); err != nil {
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
		if strings.Contains(e.Name(), ".tmp") {
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
	if err := s.Write(storedRawcall(t, "msg_01", "sess-a", "hash-a", noon)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(storedRawcall(t, "msg_02", "", "", noon)); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(filepath.Join(dir, "20260801", indexName))
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	defer f.Close()
	type line struct {
		RequestID     string `json:"request_id"`
		SessionKey    string `json:"session_key"`
		Timestamp     string `json:"timestamp"`
		ProjectIDHash string `json:"project_id_hash"`
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
	want := line{RequestID: "msg_01", SessionKey: "sess-a", Timestamp: "2026-08-01T12:00:00Z", ProjectIDHash: "hash-a"}
	if lines[0] != want {
		t.Errorf("first index line = %+v, want %+v", lines[0], want)
	}
	if lines[1].RequestID != "msg_02" || lines[1].SessionKey != "" || lines[1].ProjectIDHash != "" {
		t.Errorf("second index line = %+v", lines[1])
	}
}

func TestQuotaStopsWritesWithoutEvicting(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(storedRawcall(t, "msg_01", "", "", noon)); err != nil {
		t.Fatal(err)
	}
	s.SetQuota(s.Usage())
	err = s.Write(storedRawcall(t, "msg_02", "", "", noon))
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
	if err := s.Write(storedRawcall(t, "msg_01", "", "", noon)); err != nil {
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
	if err := s.Write(storedRawcall(t, "msg_01", "short", "", noon)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(storedRawcall(t, "msg_01", "a-much-longer-session-key", "", noon)); err != nil {
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
		if err := s.Write(parsedRawcall(t, id, "2026-08-01T12:00:00Z")); err != nil {
			t.Errorf("Write(%q) = %v, want accepted", id, err)
		}
	}
	for _, id := range []string{"", "../escape", "a/b", ".hidden"} {
		if err := s.Write(parsedRawcall(t, id, "2026-08-01T12:00:00Z")); err == nil {
			t.Errorf("Write(%q) = nil, want refused", id)
		}
	}
}

func TestWriteRefusesARawcallWithoutACaptureTimestamp(t *testing.T) {
	s, err := spool.Create(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(parsedRawcall(t, "msg_01", "")); err == nil {
		t.Error("a rawcall with no capture timestamp was accepted")
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
	deleted, err := s.DeleteWhere(func(string) bool { return true })
	if err != nil || deleted != 0 {
		t.Errorf("DeleteWhere = %d, %v, want 0, nil", deleted, err)
	}
	if err := s.Write(storedRawcall(t, "msg_01", "", "", noon)); err != nil {
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
		if err := s.Write(storedRawcall(t, "msg_01", "", "", noon)); err == nil {
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
		if err := s.Write(storedRawcall(t, "msg_01", "", "", noon)); err == nil {
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
		if err := s.Write(parsedRawcall(t, id, "2026-08-01T12:00:00Z")); err == nil {
			t.Errorf("request id %q accepted", id)
		}
	}
}

func TestSetQuotaGovernsSubsequentWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(storedRawcall(t, "msg_01", "", "", noon)); err != nil {
		t.Fatal(err)
	}

	s.SetQuota(s.Usage())
	err = s.Write(storedRawcall(t, "msg_02", "", "", noon))
	if !errors.Is(err, spool.ErrQuotaExceeded) {
		t.Fatalf("write after shrinking the quota = %v, want ErrQuotaExceeded", err)
	}

	s.SetQuota(1 << 20)
	if err := s.Write(storedRawcall(t, "msg_02", "", "", noon)); err != nil {
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

func TestSummaryReportsDaysWithoutFileNames(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, day := range []time.Time{noon, noon.Add(24 * time.Hour)} {
		env := storedRawcall(t, "msg_0"+string(rune('1'+i)), "sess-a", "hash-a", day)
		if err := s.Write(env); err != nil {
			t.Fatal(err)
		}
	}

	days, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 {
		t.Fatalf("days = %+v, want two entries", days)
	}
	var total int64
	for i, want := range []string{"20260801", "20260802"} {
		d := days[i]
		if d.Day != want || d.Records != 1 || d.Bytes <= 0 {
			t.Errorf("day %d = %+v, want %s with one record and non-zero bytes", i, d, want)
		}
		if strings.Contains(d.Day, "msg_") {
			t.Errorf("summary leaks a file name: %+v", d)
		}
		total += d.Bytes
	}
	// The summary and Usage read the same tree; they must agree.
	if total != s.Usage() {
		t.Errorf("summary bytes = %d, Usage() = %d, want them equal", total, s.Usage())
	}
}

func TestSummaryOfAnEmptySpoolIsEmpty(t *testing.T) {
	s, err := spool.Open(filepath.Join(t.TempDir(), "never-written"), 0)
	if err != nil {
		t.Fatal(err)
	}
	days, err := s.Summary()
	if err != nil || len(days) != 0 {
		t.Fatalf("Summary = %+v, %v; want empty and no error", days, err)
	}
}
