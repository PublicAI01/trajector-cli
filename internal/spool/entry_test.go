package spool_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

func collectEntries(t *testing.T, s *spool.Spool) spool.Entries {
	t.Helper()
	var got spool.Entries
	if err := s.EachEntry(func(e spool.Entry) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("EachEntry: %v", err)
	}
	return got
}

func TestEachEntryVisitsEveryStoredRecordAsOneSequence(t *testing.T) {
	s, err := spool.Create(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	if err := s.Write(rawcallOfSession(t, "req-1", sessionA, "hash-p1", at)); err != nil {
		t.Fatal(err)
	}
	seg := segment(sessionA, "hash-p1", 0, at)
	if err := s.WriteSegment(seg); err != nil {
		t.Fatal(err)
	}
	snap := snapshot(t, sessionA, "hash-p1", `{"agentId":"x"}`, at)
	if err := s.WriteMetaSnapshot(snap); err != nil {
		t.Fatal(err)
	}

	entries := collectEntries(t, s)
	if len(entries) != 3 {
		t.Fatalf("EachEntry visited %d record(s), want all three", len(entries))
	}
	if entries[0].Kind != envelope.KindRawcall || entries[0].ID != "req-1" || len(entries[0].Raw) == 0 {
		t.Errorf("first entry = %+v, want the rawcall with its bytes", entries[0])
	}
	kinds := map[string]envelope.Kind{}
	for _, e := range entries[1:] {
		kinds[e.ID] = e.Kind
	}
	if kinds[seg.RecordID] != envelope.KindSegment || kinds[snap.RecordID] != envelope.KindMetaSnapshot {
		t.Errorf("second slot read back as %+v", kinds)
	}
}

func TestEachEntryReadsBackARecordWhoseBytesNameNoKind(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	if err := s.WriteSegment(segment(sessionA, "hash-p1", 0, at)); err != nil {
		t.Fatal(err)
	}
	writeRecordFile(t, dir, at, "rec-torn", []byte("not a record"))

	for _, e := range collectEntries(t, s) {
		if e.ID != "rec-torn" {
			continue
		}
		if e.Kind.Source != envelope.KindSegment.Source || e.Kind.RecordKind != "" {
			t.Fatalf("torn record read back as %+v, want the slot named and the kind not", e.Kind)
		}
		return
	}
	t.Fatal("the torn record was not visited")
}

func TestDeleteEntriesRemovesOnlyTheSlotTheEntryNames(t *testing.T) {
	s, err := spool.Create(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	seg := segment(sessionA, "hash-p1", 0, at)
	if err := s.WriteSegment(seg); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(rawcallOfSession(t, seg.RecordID, sessionA, "hash-p1", at)); err != nil {
		t.Fatal(err)
	}

	var records spool.Entries
	for _, e := range collectEntries(t, s) {
		if e.Kind == envelope.KindSegment {
			records = append(records, e)
		}
	}
	if len(records) != 1 {
		t.Fatalf("collected %d segment(s), want the one stored", len(records))
	}
	if err := s.DeleteEntries(records); err != nil {
		t.Fatal(err)
	}

	left := collectEntries(t, s)
	if len(left) != 1 || left[0].Kind != envelope.KindRawcall || left[0].ID != seg.RecordID {
		t.Fatalf("spool holds %+v, want the rawcall of the same spelled id untouched", left)
	}
}

func TestOldestEntryIsTheEarliestRecordOfEitherKind(t *testing.T) {
	s, err := spool.Create(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.OldestEntry(); ok {
		t.Error("an empty spool named an oldest record")
	}
	early := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	late := time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)

	if err := s.Write(rawcallOfSession(t, "req-1", sessionA, "hash-p1", late)); err != nil {
		t.Fatal(err)
	}
	if at, ok := s.OldestEntry(); !ok || !at.Equal(late) {
		t.Errorf("oldest = %v (%v), want the only rawcall", at, ok)
	}
	if err := s.WriteSegment(segment(sessionA, "hash-p1", 0, early)); err != nil {
		t.Fatal(err)
	}
	if at, ok := s.OldestEntry(); !ok || !at.Equal(early) {
		t.Errorf("oldest = %v (%v), want the earlier segment", at, ok)
	}
}

func writeRecordFile(t *testing.T, dir string, at time.Time, id string, data []byte) {
	t.Helper()
	day := filepath.Join(dir, recordsDir, at.UTC().Format("20060102"))
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(day, id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEachEntryCompletesWhenRecordsVanishMidWalk(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	day := at.Format("20060102")
	for _, id := range []string{"req-1", "req-2"} {
		if err := s.Write(rawcallOfSession(t, id, sessionA, "hash-p1", at)); err != nil {
			t.Fatal(err)
		}
	}
	segments := []envelope.Segment{segment(sessionA, "hash-p1", 0, at), segment(sessionA, "hash-p1", 1, at)}
	for _, seg := range segments {
		if err := s.WriteSegment(seg); err != nil {
			t.Fatal(err)
		}
	}

	var visited []string
	err = s.EachEntry(func(e spool.Entry) error {
		visited = append(visited, e.ID)
		if e.ID == "req-1" {
			removeStoredFile(t, filepath.Join(dir, day, "req-2.json"))
			return nil
		}
		for _, seg := range segments {
			if seg.RecordID != e.ID {
				removeStoredFile(t, filepath.Join(dir, recordsDir, day, seg.RecordID+".json"))
			}
		}
		return nil
	})

	if err != nil {
		t.Fatalf("EachEntry = %v, want the walk to complete over the records still on disk", err)
	}
	if len(visited) != 2 || visited[0] != "req-1" || visited[1] == "req-2" {
		t.Fatalf("visited %v, want the rawcall that stayed and one segment", visited)
	}
}

func removeStoredFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
