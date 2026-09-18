package spool_test

import (
	"bytes"
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

const (
	sessionA = "0a1b2c3d-1111-4aaa-8aaa-000000000001"
	sessionB = "0a1b2c3d-2222-4bbb-8bbb-000000000002"
)

// recordsDir restates the documented on-disk home of the second slot.
const recordsDir = "records"

func captureAt(at time.Time, projectHash string) envelope.Capture {
	return envelope.Capture{
		ClientVersion: "0.0.0-test",
		Timestamp:     at.UTC().Format(time.RFC3339Nano),
		ProjectIDHash: projectHash,
		Injection:     envelope.InjectionProxy,
	}
}

func segment(sessionID, projectHash string, index int, at time.Time) envelope.Segment {
	return envelope.NewSegment(sessionID, "", index, captureAt(at, projectHash), `{"type":"user","message":{"role":"user","content":"hi"}}`+"\n")
}

func snapshot(t *testing.T, sessionID, projectHash, content string, at time.Time) envelope.MetaSnapshot {
	t.Helper()
	snap, err := envelope.NewMetaSnapshot(sessionID, "subagents/agent-0000.meta.json", captureAt(at, projectHash), []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func gitSnapshot(sessionID, projectHash, head string, at time.Time) envelope.GitSnapshot {
	return envelope.NewGitSnapshot(sessionID, "SessionStart", envelope.TriggerSessionStart,
		captureAt(at, projectHash), "main", head, envelope.CommitOrNone(""), envelope.CommitOrNone(""), nil)
}

// userIDFor spells metadata.user_id the way a request body carries it:
// a JSON object serialized into a string.
func userIDFor(sessionID string) string {
	v := map[string]string{"device_id": "0000deadbeef", "account_uuid": "0a1b2c3d-0000-4000-8000-000000000000", "session_id": sessionID}
	b, _ := json.Marshal(v)
	return string(b)
}

func rawcallOfSession(t *testing.T, id, sessionID, projectHash string, at time.Time) envelope.Envelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{"metadata": map[string]string{"user_id": userIDFor(sessionID)}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Record(envelope.Observation{
		ProjectIDHash:    projectHash,
		At:               at,
		Request:          body,
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

func collectRecords(t *testing.T, s *spool.Spool) []spool.Record {
	t.Helper()
	var got []spool.Record
	if err := s.EachRecord(func(r spool.Record) error {
		got = append(got, r)
		return nil
	}); err != nil {
		t.Fatalf("EachRecord: %v", err)
	}
	return got
}

func recordIDs(records []spool.Record) []string {
	ids := make([]string, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.ID)
	}
	return ids
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatal(err)
	return false
}

func TestSpool_RecordsLiveApartFromRawcalls(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(storedRawcall(t, "msg_01", "sess-a", "hash-a", noon)); err != nil {
		t.Fatal(err)
	}
	seg := segment(sessionA, "hash-a", 0, noon)
	if err := s.WriteSegment(seg); err != nil {
		t.Fatal(err)
	}
	snap := snapshot(t, sessionA, "hash-a", `{"agentId":"0000","name":"Explore"}`, noon)
	if err := s.WriteMetaSnapshot(snap); err != nil {
		t.Fatal(err)
	}

	segPath := filepath.Join(dir, recordsDir, "20260801", seg.RecordID+".json")
	snapPath := filepath.Join(dir, recordsDir, "20260801", snap.RecordID+".json")
	for _, path := range []string{filepath.Join(dir, "20260801", "msg_01.json"), segPath, snapPath} {
		if !fileExists(t, path) {
			t.Errorf("%s not stored", path)
		}
	}
	want, _ := seg.Bytes()
	if got, _ := os.ReadFile(segPath); !bytes.Equal(got, want) {
		t.Errorf("stored segment bytes = %q, want %q", got, want)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(segPath); info.Mode().Perm() != 0o600 {
			t.Errorf("record file mode = %o, want 600", info.Mode().Perm())
		}
		if info, _ := os.Stat(filepath.Join(dir, recordsDir, "20260801")); info.Mode().Perm() != 0o700 {
			t.Errorf("record day directory mode = %o, want 700", info.Mode().Perm())
		}
	}

	rawcallIndex, err := os.ReadFile(filepath.Join(dir, "20260801", indexName))
	if err != nil {
		t.Fatal(err)
	}
	if lines := bytes.Count(rawcallIndex, []byte("\n")); lines != 1 || bytes.Contains(rawcallIndex, []byte(seg.RecordID)) {
		t.Errorf("rawcall index = %q, want exactly the one rawcall line", rawcallIndex)
	}

	recordIndex, err := os.ReadFile(filepath.Join(dir, recordsDir, "20260801", indexName))
	if err != nil {
		t.Fatal(err)
	}
	type line struct {
		RecordID      string `json:"record_id"`
		RecordKind    string `json:"record_kind"`
		SessionID     string `json:"session_id"`
		ProjectIDHash string `json:"project_id_hash"`
		Size          int64  `json:"size"`
		Timestamp     string `json:"timestamp"`
	}
	var lines []line
	for _, raw := range bytes.Split(bytes.TrimSpace(recordIndex), []byte("\n")) {
		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatalf("record index line %q: %v", raw, err)
		}
		lines = append(lines, l)
	}
	wantLines := []line{
		{RecordID: seg.RecordID, RecordKind: "segment", SessionID: sessionA, ProjectIDHash: "hash-a", Size: int64(len(want)), Timestamp: "2026-08-01T12:00:00Z"},
		{RecordID: snap.RecordID, RecordKind: "meta_snapshot", SessionID: sessionA, ProjectIDHash: "hash-a", Size: lines[1].Size, Timestamp: "2026-08-01T12:00:00Z"},
	}
	if len(lines) != 2 || lines[0] != wantLines[0] || lines[1] != wantLines[1] || lines[1].Size <= 0 {
		t.Errorf("record index lines = %+v, want %+v", lines, wantLines)
	}

	if got := collect(t, s); len(got) != 1 || got[0].RequestID != "msg_01" {
		t.Errorf("Each visited %+v, want only the rawcall", got)
	}
	records := collectRecords(t, s)
	if len(records) != 2 {
		t.Fatalf("EachRecord visited %d records, want 2", len(records))
	}
	for _, r := range records {
		if r.SessionID != sessionA || r.ProjectIDHash != "hash-a" || !r.Timestamp.Equal(noon) || len(r.Raw) == 0 {
			t.Errorf("record read back as %+v", r)
		}
	}
	days, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range days {
		if d.Day == recordsDir {
			t.Errorf("Summary lists the record slot as a day: %+v", days)
		}
	}
}

func TestSpool_QuotaStopsBothSlotsWithoutEviction(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(storedRawcall(t, "msg_01", "", "hash-a", noon)); err != nil {
		t.Fatal(err)
	}
	kept := segment(sessionA, "hash-a", 0, noon)
	if err := s.WriteSegment(kept); err != nil {
		t.Fatal(err)
	}
	s.SetQuota(s.Usage())

	refused := segment(sessionA, "hash-a", 1, noon)
	refusedSnap := snapshot(t, sessionA, "hash-a", `{"agentId":"0001"}`, noon)
	tests := []struct {
		name  string
		write func() error
		path  string
	}{
		{"rawcall", func() error { return s.Write(storedRawcall(t, "msg_02", "", "hash-a", noon)) }, filepath.Join(dir, "20260801", "msg_02.json")},
		{"segment", func() error { return s.WriteSegment(refused) }, filepath.Join(dir, recordsDir, "20260801", refused.RecordID+".json")},
		{"snapshot", func() error { return s.WriteMetaSnapshot(refusedSnap) }, filepath.Join(dir, recordsDir, "20260801", refusedSnap.RecordID+".json")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.write(); !errors.Is(err, spool.ErrQuotaExceeded) {
				t.Fatalf("write on a full spool = %v, want ErrQuotaExceeded", err)
			}
			if fileExists(t, tc.path) {
				t.Error("refused write left a file behind")
			}
			if !fileExists(t, filepath.Join(dir, "20260801", "msg_01.json")) {
				t.Error("existing rawcall evicted")
			}
			if !fileExists(t, filepath.Join(dir, recordsDir, "20260801", kept.RecordID+".json")) {
				t.Error("existing segment evicted")
			}
		})
	}
	if err := s.Writable(); !errors.Is(err, spool.ErrQuotaExceeded) {
		t.Errorf("Writable on a full spool = %v, want ErrQuotaExceeded", err)
	}
}

func TestSpool_WriteSegmentIsIdempotentPerRecordID(t *testing.T) {
	tests := []struct {
		name  string
		write func(t *testing.T, s *spool.Spool, at time.Time) string
	}{
		{"segment", func(t *testing.T, s *spool.Spool, at time.Time) string {
			seg := segment(sessionA, "hash-a", 0, at)
			if err := s.WriteSegment(seg); err != nil {
				t.Fatal(err)
			}
			return seg.RecordID
		}},
		{"snapshot", func(t *testing.T, s *spool.Spool, at time.Time) string {
			snap := snapshot(t, sessionA, "hash-a", `{"agentId":"0000"}`, at)
			if err := s.WriteMetaSnapshot(snap); err != nil {
				t.Fatal(err)
			}
			return snap.RecordID
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := spool.Create(dir, 0)
			if err != nil {
				t.Fatal(err)
			}
			id := tc.write(t, s, noon)
			path := filepath.Join(dir, recordsDir, "20260801", id+".json")
			first, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			usage := s.Usage()

			// The same record resent later carries a later capture time
			// and would land in another day; it must still be recognized.
			if again := tc.write(t, s, noon.Add(36*time.Hour)); again != id {
				t.Fatalf("record id changed on resend: %s vs %s", again, id)
			}
			second, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !second.ModTime().Equal(first.ModTime()) || second.Size() != first.Size() {
				t.Error("resending the same record rewrote its file")
			}
			if fileExists(t, filepath.Join(dir, recordsDir, "20260803")) {
				t.Error("resending the same record stored it a second time")
			}
			index, _ := os.ReadFile(filepath.Join(dir, recordsDir, "20260801", indexName))
			if bytes.Count(index, []byte(id)) != 1 {
				t.Errorf("index = %q, want the record listed once", index)
			}
			if s.Usage() != usage {
				t.Errorf("Usage = %d after resend, want unchanged %d", s.Usage(), usage)
			}
			if got := collectRecords(t, s); len(got) != 1 {
				t.Errorf("EachRecord visited %d records, want 1", len(got))
			}
		})
	}
}

func TestSpool_WriteRecordRefusesWhatItCannotStore(t *testing.T) {
	s, err := spool.Create(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	good := segment(sessionA, "hash-a", 0, noon)
	tests := []struct {
		name  string
		write func() error
	}{
		{"segment with a record id that escapes its directory", func() error {
			seg := good
			seg.RecordID = "../escape"
			return s.WriteSegment(seg)
		}},
		{"segment without a capture timestamp", func() error {
			seg := good
			seg.Capture.Timestamp = ""
			return s.WriteSegment(seg)
		}},
		{"segment without a session id", func() error {
			seg := good
			seg.SessionID = ""
			return s.WriteSegment(seg)
		}},
		{"segment declaring another kind", func() error {
			seg := good
			seg.RecordKind = "meta_snapshot"
			return s.WriteSegment(seg)
		}},
		{"snapshot declaring another kind", func() error {
			snap := snapshot(t, sessionA, "hash-a", `{}`, noon)
			snap.Source = "proxy"
			return s.WriteMetaSnapshot(snap)
		}},
		{"observation declaring another kind", func() error {
			snap := gitSnapshot(sessionA, "hash-a", headA, noon)
			snap.RecordKind = "segment"
			return s.WriteGitSnapshot(snap)
		}},
		{"observation without a session id", func() error {
			snap := gitSnapshot("", "hash-a", headA, noon)
			return s.WriteGitSnapshot(snap)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.write(); err == nil {
				t.Error("write accepted")
			}
		})
	}
	if got := collectRecords(t, s); len(got) != 0 {
		t.Errorf("refused writes stored %d records", len(got))
	}
}

func TestSpool_EachRecordOrdersByDayThenID(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day1 := noon
	day2 := noon.Add(24 * time.Hour)
	later := []envelope.Segment{
		segment(sessionB, "hash-a", 0, day2),
		segment(sessionA, "hash-a", 3, day2),
	}
	earlier := []envelope.Segment{
		segment(sessionB, "hash-a", 1, day1),
		segment(sessionA, "hash-a", 0, day1),
		segment(sessionA, "hash-a", 2, day1),
	}
	for _, seg := range append(append([]envelope.Segment{}, later...), earlier...) {
		if err := s.WriteSegment(seg); err != nil {
			t.Fatal(err)
		}
	}
	snap := snapshot(t, sessionA, "hash-a", `{"agentId":"0000"}`, day1)
	if err := s.WriteMetaSnapshot(snap); err != nil {
		t.Fatal(err)
	}

	got := recordIDs(collectRecords(t, s))
	if len(got) != 6 {
		t.Fatalf("EachRecord visited %d records, want 6", len(got))
	}
	for i := 1; i < 4; i++ {
		if got[i-1] >= got[i] {
			t.Errorf("day one out of id order: %v", got[:4])
		}
	}
	if got[4] >= got[5] {
		t.Errorf("day two out of id order: %v", got[4:])
	}
	dayOne := map[string]bool{earlier[0].RecordID: true, earlier[1].RecordID: true, earlier[2].RecordID: true, snap.RecordID: true}
	for _, id := range got[:4] {
		if !dayOne[id] {
			t.Errorf("record %s of a later day visited before the earlier day was done", id)
		}
	}

	stop := errors.New("stop")
	visited := 0
	err = s.EachRecord(func(spool.Record) error {
		visited++
		return stop
	})
	if err != stop || visited != 1 {
		t.Errorf("EachRecord = %v after %d visits, want the visitor error immediately", err, visited)
	}
}

func TestSpool_OldestRecordReportsTheEarliestCapture(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.OldestRecord(); ok {
		t.Fatal("an empty spool reported an oldest record")
	}
	early := noon.Add(-3 * time.Hour)
	if err := s.WriteSegment(segment(sessionA, "hash-a", 1, noon)); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSegment(segment(sessionA, "hash-a", 0, early)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(storedRawcall(t, "msg_01", "", "hash-a", noon.Add(-48*time.Hour))); err != nil {
		t.Fatal(err)
	}
	oldest, ok := s.OldestRecord()
	if !ok || !oldest.Equal(early) {
		t.Errorf("OldestRecord = %v, %v, want %v", oldest, ok, early)
	}
	if rawcallOldest, ok := s.Oldest(); !ok || !rawcallOldest.Equal(noon.Add(-48*time.Hour)) {
		t.Errorf("Oldest = %v, %v, want the rawcall slot's own time", rawcallOldest, ok)
	}
}

func TestSpool_SummarySeparatesSlots(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day1 := noon
	day2 := noon.Add(24 * time.Hour)
	if err := s.Write(storedRawcall(t, "msg_01", "sess-a", "hash-a", day1)); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSegment(segment(sessionA, "hash-a", 0, day1)); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSegment(segment(sessionA, "hash-a", 1, day1)); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMetaSnapshot(snapshot(t, sessionA, "hash-a", `{"agentId":"0000"}`, day2)); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteGitSnapshot(gitSnapshot(sessionA, "hash-a", headA, day2)); err != nil {
		t.Fatal(err)
	}

	days, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 {
		t.Fatalf("Summary = %+v, want two days", days)
	}
	tests := []struct {
		day                                         string
		rawcalls, segments, snapshots, gitSnapshots int
		rawcallBytes, recordBytesZero               bool
	}{
		{"20260801", 1, 2, 0, 0, true, false},
		{"20260802", 0, 0, 1, 1, false, false},
	}
	var total int64
	for i, tc := range tests {
		d := days[i]
		if d.Day != tc.day || d.Rawcalls != tc.rawcalls || d.Segments != tc.segments || d.Snapshots != tc.snapshots || d.GitSnapshots != tc.gitSnapshots {
			t.Errorf("day %s = %+v, want %+v", tc.day, d, tc)
		}
		if want := tc.rawcalls + tc.segments + tc.snapshots + tc.gitSnapshots; d.Total() != want {
			t.Errorf("day %s Total() = %d, want %d", tc.day, d.Total(), want)
		}
		if (d.Bytes > 0) != tc.rawcallBytes {
			t.Errorf("day %s rawcall bytes = %d", tc.day, d.Bytes)
		}
		if (d.RecordBytes == 0) != tc.recordBytesZero {
			t.Errorf("day %s record bytes = %d", tc.day, d.RecordBytes)
		}
		total += d.Bytes + d.RecordBytes
	}
	if total != s.Usage() {
		t.Errorf("summary bytes = %d, Usage() = %d, want them equal", total, s.Usage())
	}
}

func TestSpool_DeleteSessionRemovesBothSides(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day1, day2 := noon, noon.Add(24*time.Hour)
	if err := s.Write(rawcallOfSession(t, "msg_a1", sessionA, "hash-a", day1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(rawcallOfSession(t, "msg_a2", sessionA, "hash-a", day2)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(rawcallOfSession(t, "msg_b1", sessionB, "hash-a", day1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(storedRawcall(t, "msg_none", "", "hash-a", day1)); err != nil {
		t.Fatal(err)
	}
	segA := segment(sessionA, "hash-a", 0, day1)
	segB := segment(sessionB, "hash-a", 0, day1)
	snapA := snapshot(t, sessionA, "hash-a", `{"agentId":"0000"}`, day2)
	for _, w := range []func() error{
		func() error { return s.WriteSegment(segA) },
		func() error { return s.WriteSegment(segB) },
		func() error { return s.WriteMetaSnapshot(snapA) },
	} {
		if err := w(); err != nil {
			t.Fatal(err)
		}
	}

	rawcalls, records, err := s.DeleteSession(sessionA)
	if err != nil {
		t.Fatal(err)
	}
	if rawcalls != 2 || records != 2 {
		t.Errorf("DeleteSession = %d rawcalls, %d records; want 2, 2", rawcalls, records)
	}
	gone := []string{
		filepath.Join(dir, "20260801", "msg_a1.json"),
		filepath.Join(dir, "20260802", "msg_a2.json"),
		filepath.Join(dir, recordsDir, "20260801", segA.RecordID+".json"),
		filepath.Join(dir, recordsDir, "20260802", snapA.RecordID+".json"),
	}
	for _, path := range gone {
		if fileExists(t, path) {
			t.Errorf("%s survived DeleteSession", path)
		}
	}
	kept := []string{
		filepath.Join(dir, "20260801", "msg_b1.json"),
		filepath.Join(dir, "20260801", "msg_none.json"),
		filepath.Join(dir, recordsDir, "20260801", segB.RecordID+".json"),
	}
	for _, path := range kept {
		if !fileExists(t, path) {
			t.Errorf("%s of another session was deleted", path)
		}
	}
	index, _ := os.ReadFile(filepath.Join(dir, recordsDir, "20260801", indexName))
	if bytes.Contains(index, []byte(segA.RecordID)) || !bytes.Contains(index, []byte(segB.RecordID)) {
		t.Errorf("record index after delete = %q", index)
	}
	rawcallIndex, _ := os.ReadFile(filepath.Join(dir, "20260801", indexName))
	if bytes.Contains(rawcallIndex, []byte("msg_a1")) || !bytes.Contains(rawcallIndex, []byte("msg_b1")) {
		t.Errorf("rawcall index after delete = %q", rawcallIndex)
	}

	reopened, err := spool.Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Usage() != s.Usage() {
		t.Errorf("tracked usage %d diverged from on-disk usage %d", s.Usage(), reopened.Usage())
	}

	if _, _, err := s.DeleteSession(""); err == nil {
		t.Error("DeleteSession with no id was accepted")
	}
	if !fileExists(t, filepath.Join(dir, "20260801", "msg_none.json")) {
		t.Error("a rawcall with no session identity was deleted by an empty id")
	}
}

func TestSpool_DeleteSessionReadsRawcallBodiesTheIndexMissed(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(rawcallOfSession(t, "msg_a1", sessionA, "hash-a", noon)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(rawcallOfSession(t, "msg_b1", sessionB, "hash-a", noon)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "20260801", indexName)); err != nil {
		t.Fatal(err)
	}
	garbled := filepath.Join(dir, "20260801", "msg_x.json")
	if err := os.WriteFile(garbled, []byte("not a rawcall"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawcalls, records, err := s.DeleteSession(sessionA)
	if err != nil || rawcalls != 1 || records != 0 {
		t.Fatalf("DeleteSession = %d, %d, %v; want 1, 0, nil", rawcalls, records, err)
	}
	if fileExists(t, filepath.Join(dir, "20260801", "msg_a1.json")) || !fileExists(t, filepath.Join(dir, "20260801", "msg_b1.json")) || !fileExists(t, garbled) {
		t.Error("DeleteSession without an index deleted the wrong rawcalls")
	}
}

func TestSpool_DeleteProjectCoversBothSlots(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(storedRawcall(t, "msg_a1", "sess", "hash-a", noon)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(storedRawcall(t, "msg_b1", "sess", "hash-b", noon)); err != nil {
		t.Fatal(err)
	}
	segA := segment(sessionA, "hash-a", 0, noon)
	segB := segment(sessionB, "hash-b", 0, noon)
	snapA := snapshot(t, sessionA, "hash-a", `{"agentId":"0000"}`, noon)
	for _, w := range []func() error{
		func() error { return s.WriteSegment(segA) },
		func() error { return s.WriteSegment(segB) },
		func() error { return s.WriteMetaSnapshot(snapA) },
	} {
		if err := w(); err != nil {
			t.Fatal(err)
		}
	}
	unattributed := filepath.Join(dir, recordsDir, "20260801", "seg_unattributed.json")
	if err := os.WriteFile(unattributed, []byte("no project here"), 0o600); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.DeleteProject("hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 3 {
		t.Errorf("DeleteProject = %d, want 3 across both slots", deleted)
	}
	for _, path := range []string{
		filepath.Join(dir, "20260801", "msg_a1.json"),
		filepath.Join(dir, recordsDir, "20260801", segA.RecordID+".json"),
		filepath.Join(dir, recordsDir, "20260801", snapA.RecordID+".json"),
	} {
		if fileExists(t, path) {
			t.Errorf("%s survived DeleteProject", path)
		}
	}
	for _, path := range []string{
		filepath.Join(dir, "20260801", "msg_b1.json"),
		filepath.Join(dir, recordsDir, "20260801", segB.RecordID+".json"),
		unattributed,
	} {
		if !fileExists(t, path) {
			t.Errorf("%s was deleted by another project's withdrawal", path)
		}
	}
	if deleted, err := s.DeleteProject(""); err != nil || deleted != 0 {
		t.Errorf("DeleteProject(\"\") = %d, %v; want nothing deleted", deleted, err)
	}
	if deleted, err := s.DeleteProject("hash-b"); err != nil || deleted != 2 {
		t.Errorf("DeleteProject(hash-b) = %d, %v; want 2", deleted, err)
	}
	if !fileExists(t, unattributed) {
		t.Error("a record attributable to no project was deleted")
	}
	reopened, err := spool.Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Usage() != s.Usage() {
		t.Errorf("tracked usage %d diverged from on-disk usage %d", s.Usage(), reopened.Usage())
	}
}

func TestSpool_DeleteRecordsWhereMatchesOnTheIndexAlone(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	segA := segment(sessionA, "hash-a", 0, noon)
	segB := segment(sessionB, "hash-a", 0, noon)
	if err := s.WriteSegment(segA); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSegment(segB); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, recordsDir, "20260801", segA.RecordID+".json"), []byte("stopped parsing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, recordsDir, "20260801", "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	var seen []spool.Record
	deleted, err := s.DeleteRecordsWhere(func(r spool.Record) bool {
		seen = append(seen, r)
		return r.ID == segA.RecordID
	})
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteRecordsWhere = %d, %v; want 1, nil", deleted, err)
	}
	if len(seen) != 2 {
		t.Fatalf("matcher saw %d records, want 2", len(seen))
	}
	for _, r := range seen {
		if r.Raw != nil {
			t.Errorf("matcher was handed record bytes for %s", r.ID)
		}
		if r.SessionID == "" || r.Kind != "segment" || r.Timestamp.IsZero() {
			t.Errorf("matcher saw an undescribed record: %+v", r)
		}
	}
	if fileExists(t, filepath.Join(dir, recordsDir, "20260801", segA.RecordID+".json")) {
		t.Error("matched record still present")
	}
	if !fileExists(t, filepath.Join(dir, recordsDir, "20260801", segB.RecordID+".json")) || !fileExists(t, filepath.Join(dir, recordsDir, "20260801", "notes.txt")) {
		t.Error("an unmatched file was removed")
	}
	if deleted, err := s.DeleteRecordsWhere(func(spool.Record) bool { return true }); err != nil || deleted != 1 {
		t.Errorf("second DeleteRecordsWhere = %d, %v; want the remaining record", deleted, err)
	}
}

func TestSpool_RecordIndexRebuildsFromFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	seg := segment(sessionA, "hash-a", 0, noon)
	snap := snapshot(t, sessionB, "hash-b", `{"agentId":"0000"}`, noon)
	if err := s.WriteSegment(seg); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMetaSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(dir, recordsDir, "20260801", indexName)
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}

	t.Run("EachRecord describes records from their bytes", func(t *testing.T) {
		got := collectRecords(t, s)
		if len(got) != 2 {
			t.Fatalf("EachRecord visited %d records, want 2", len(got))
		}
		want := map[string]spool.Record{
			seg.RecordID:  {ID: seg.RecordID, Kind: "segment", SessionID: sessionA, ProjectIDHash: "hash-a", Timestamp: noon},
			snap.RecordID: {ID: snap.RecordID, Kind: "meta_snapshot", SessionID: sessionB, ProjectIDHash: "hash-b", Timestamp: noon},
		}
		for _, r := range got {
			w := want[r.ID]
			if r.Kind != w.Kind || r.SessionID != w.SessionID || r.ProjectIDHash != w.ProjectIDHash || !r.Timestamp.Equal(w.Timestamp) {
				t.Errorf("rebuilt record = %+v, want %+v", r, w)
			}
		}
	})
	t.Run("OldestRecord falls back to file time", func(t *testing.T) {
		if oldest, ok := s.OldestRecord(); !ok || oldest.IsZero() {
			t.Errorf("OldestRecord = %v, %v", oldest, ok)
		}
	})
	t.Run("Summary still counts by kind", func(t *testing.T) {
		days, err := s.Summary()
		if err != nil || len(days) != 1 || days[0].Segments != 1 || days[0].Snapshots != 1 {
			t.Errorf("Summary = %+v, %v", days, err)
		}
	})
	t.Run("a rewritten index lists only what is on disk", func(t *testing.T) {
		next := segment(sessionA, "hash-a", 1, noon)
		if err := s.WriteSegment(next); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DeleteRecordsWhere(func(r spool.Record) bool { return r.SessionID == sessionB }); err != nil {
			t.Fatal(err)
		}
		index, err := os.ReadFile(indexPath)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(index, []byte(snap.RecordID)) || !bytes.Contains(index, []byte(next.RecordID)) {
			t.Errorf("index after delete = %q", index)
		}
		if fileExists(t, filepath.Join(dir, recordsDir, "20260801", snap.RecordID+".json")) {
			t.Error("DeleteRecordsWhere by session did not find an unindexed record")
		}
	})
}

func TestSpool_UnreadableRecordIsSetAside(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	seg := segment(sessionA, "hash-a", 0, noon)
	if err := s.WriteSegment(seg); err != nil {
		t.Fatal(err)
	}
	dayDir := filepath.Join(dir, recordsDir, "20260801")
	declared, err := seg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		id   string
		body string
	}{
		{"not json", "seg_broken1", "not json at all"},
		{"json of no known kind", "seg_broken2", `{"source":"elsewhere","record_kind":"other"}`},
		{"segment of an unknown schema version", "seg_broken3", strings.Replace(string(declared), `"schema_version":"3"`, `"schema_version":"9"`, 1)},
	}
	for _, tc := range tests {
		if err := os.WriteFile(filepath.Join(dayDir, tc.id+".json"), []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got := collectRecords(t, s)
	if len(got) != 4 {
		t.Fatalf("EachRecord visited %d records, want the readable one and three set aside", len(got))
	}
	byID := map[string]spool.Record{}
	for _, r := range got {
		byID[r.ID] = r
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := byID[tc.id]
			if !ok {
				t.Fatal("record not visited")
			}
			if r.Kind != "" || r.SessionID != "" || r.ProjectIDHash != "" {
				t.Errorf("unreadable record was attributed: %+v", r)
			}
			if r.Timestamp.IsZero() || string(r.Raw) != tc.body {
				t.Errorf("unreadable record read back as %+v, want its file time and bytes", r)
			}
		})
	}
	if r := byID[seg.RecordID]; r.SessionID != sessionA {
		t.Errorf("readable record lost its attribution: %+v", r)
	}

	if deleted, err := s.DeleteProject("hash-a"); err != nil || deleted != 1 {
		t.Errorf("DeleteProject = %d, %v; want only the attributable record", deleted, err)
	}
	if _, records, err := s.DeleteSession(sessionA); err != nil || records != 0 {
		t.Errorf("DeleteSession = %d records, %v; want none of the set-aside records", records, err)
	}
	for _, tc := range tests {
		if !fileExists(t, filepath.Join(dayDir, tc.id+".json")) {
			t.Errorf("%s was deleted by attribution it does not carry", tc.id)
		}
	}
	if deleted, err := s.DeleteRecordsWhere(func(r spool.Record) bool { return strings.HasPrefix(r.ID, "seg_broken") }); err != nil || deleted != 3 {
		t.Errorf("DeleteRecordsWhere by id = %d, %v; want the three set aside", deleted, err)
	}
}

func TestSpool_ForeignRecordWritesConvergeAcrossHandles(t *testing.T) {
	dir := t.TempDir()
	a, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := spool.Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteSegment(segment(sessionA, "hash-a", 0, noon)); err != nil {
		t.Fatal(err)
	}
	if a.Usage() != b.Usage() {
		t.Errorf("handle A usage = %d after a foreign record write, handle B usage = %d", a.Usage(), b.Usage())
	}
	if err := b.WriteSegment(segment(sessionA, "hash-a", 1, noon)); err != nil {
		t.Fatal(err)
	}
	if a.Usage() != b.Usage() {
		t.Errorf("handle A usage = %d after a second foreign write into an existing day, handle B usage = %d", a.Usage(), b.Usage())
	}

	a.SetQuota(a.Usage())
	if err := a.WriteSegment(segment(sessionA, "hash-a", 2, noon)); !errors.Is(err, spool.ErrQuotaExceeded) {
		t.Fatalf("write on a full spool = %v, want ErrQuotaExceeded", err)
	}
	if _, err := b.DeleteRecordsWhere(func(spool.Record) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if err := a.WriteSegment(segment(sessionA, "hash-a", 2, noon)); err != nil {
		t.Errorf("write after a foreign delete = %v, want success", err)
	}
}

func TestSpool_StrandedRecordTempIsReclaimed(t *testing.T) {
	dir := t.TempDir()
	path := strandedTemp(t, filepath.Join(dir, recordsDir, "20260801"), "seg_stranded", 4096)
	s, err := spool.Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fileExists(t, path) {
		t.Error("a stranded record temp survived Open")
	}
	if s.Usage() != 0 {
		t.Errorf("Usage = %d, want 0", s.Usage())
	}
}

func TestSpool_RecordSlotStartsEmptyOnAnUnwrittenSpool(t *testing.T) {
	s, err := spool.Open(filepath.Join(t.TempDir(), "never-written"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := collectRecords(t, s); len(got) != 0 {
		t.Errorf("EachRecord visited %d records", len(got))
	}
	if _, ok := s.OldestRecord(); ok {
		t.Error("an unwritten spool reported an oldest record")
	}
	if deleted, err := s.DeleteRecordsWhere(func(spool.Record) bool { return true }); err != nil || deleted != 0 {
		t.Errorf("DeleteRecordsWhere = %d, %v", deleted, err)
	}
	if rawcalls, records, err := s.DeleteSession(sessionA); err != nil || rawcalls != 0 || records != 0 {
		t.Errorf("DeleteSession = %d, %d, %v", rawcalls, records, err)
	}
}

func TestSessionIDFromUserID(t *testing.T) {
	tests := []struct {
		name   string
		userID string
		want   string
		ok     bool
	}{
		{"json object with a session id", userIDFor(sessionA), sessionA, true},
		{"json object without a session id", `{"device_id":"0000deadbeef","account_uuid":"0a1b2c3d-0000-4000-8000-000000000000"}`, "", false},
		{"json object with an empty session id", `{"session_id":""}`, "", false},
		{"json that does not parse", `{"session_id":`, "", false},
		{"flat form ending in a session id", "user_0000deadbeef_account_0a1b2c3d-0000-4000-8000-000000000000_session_" + sessionB, sessionB, true},
		{"flat form with nothing after the marker", "user_0000deadbeef_session_", "", false},
		{"flat form without a session", "user_0000deadbeef_account_0a1b2c3d-0000-4000-8000-000000000000", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := spool.SessionIDFromUserID(tc.userID)
			if got != tc.want || ok != tc.ok {
				t.Errorf("SessionIDFromUserID(%q) = %q, %v; want %q, %v", tc.userID, got, ok, tc.want, tc.ok)
			}
		})
	}
}

const (
	headA = "d0cf90f327430f11f8a68493a58f402fa11d7c9e"
	headB = "4d0071c7e54967da4d11e6a397df9844797bdf81"
)

func TestSpool_StoresObservationsBesideTheOtherRecordsOfTheirSlot(t *testing.T) {
	s, err := spool.Create(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSegment(segment(sessionA, "hash-a", 0, noon)); err != nil {
		t.Fatal(err)
	}
	snap := gitSnapshot(sessionA, "hash-a", headA, noon)
	if err := s.WriteGitSnapshot(snap); err != nil {
		t.Fatal(err)
	}

	kinds := map[string]string{}
	for _, r := range collectRecords(t, s) {
		kinds[r.ID] = r.Kind
		if r.SessionID != sessionA || r.ProjectIDHash != "hash-a" {
			t.Errorf("record %s = %+v, want it attributed to its session and project", r.ID, r)
		}
	}
	if kinds[snap.RecordID] != "git_snapshot" {
		t.Errorf("records = %v, want the observation stored under its own kind", kinds)
	}
	if len(kinds) != 2 {
		t.Errorf("records = %v, want both slots' records", kinds)
	}
}

func TestSpool_WriteGitSnapshotIsIdempotentPerRecordID(t *testing.T) {
	s, err := spool.Create(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	snap := gitSnapshot(sessionA, "hash-a", headA, noon)
	for range 3 {
		if err := s.WriteGitSnapshot(snap); err != nil {
			t.Fatal(err)
		}
	}
	if got := collectRecords(t, s); len(got) != 1 {
		t.Errorf("the same observation stored three times left %d records", len(got))
	}
	if err := s.WriteGitSnapshot(gitSnapshot(sessionA, "hash-a", headB, noon)); err != nil {
		t.Fatal(err)
	}
	if got := collectRecords(t, s); len(got) != 2 {
		t.Errorf("a second observation left %d records", len(got))
	}
}

func TestSpool_DeletingASessionTakesItsObservationsToo(t *testing.T) {
	s, err := spool.Create(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteGitSnapshot(gitSnapshot(sessionA, "hash-a", headA, noon)); err != nil {
		t.Fatal(err)
	}
	kept := gitSnapshot(sessionB, "hash-a", headB, noon)
	if err := s.WriteGitSnapshot(kept); err != nil {
		t.Fatal(err)
	}
	if _, records, err := s.DeleteSession(sessionA); err != nil || records != 1 {
		t.Fatalf("DeleteSession = %d, %v; want the session's one observation", records, err)
	}
	got := collectRecords(t, s)
	if len(got) != 1 || got[0].ID != kept.RecordID {
		t.Errorf("records = %+v, want only the other session's observation", got)
	}
}
