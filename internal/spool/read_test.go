package spool_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/spool"
)

func collect(t *testing.T, s *spool.Spool) []spool.Rawcall {
	t.Helper()
	var got []spool.Rawcall
	if err := s.Each(func(r spool.Rawcall) error {
		got = append(got, r)
		return nil
	}); err != nil {
		t.Fatalf("Each: %v", err)
	}
	return got
}

func TestEachReadsBackWhatWasWritten(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day1 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	first := writeRawcall(t, s, "req-a1", "hash-a", day1)
	writeRawcall(t, s, "req-b1", "hash-b", day2)

	got := collect(t, s)
	if len(got) != 2 {
		t.Fatalf("Each visited %d rawcalls, want 2", len(got))
	}
	if got[0].RequestID != "req-a1" || got[1].RequestID != "req-b1" {
		t.Errorf("visit order = %s, %s, want oldest day first", got[0].RequestID, got[1].RequestID)
	}
	if string(got[0].Data) != string(first) {
		t.Errorf("Data = %s, want the stored bytes", got[0].Data)
	}
	if got[0].SessionKey != "sess" {
		t.Errorf("SessionKey = %q, want the indexed value", got[0].SessionKey)
	}
	if !got[0].Timestamp.Equal(day1) {
		t.Errorf("Timestamp = %v, want %v", got[0].Timestamp, day1)
	}
	if got[0].Size != int64(len(first)) {
		t.Errorf("Size = %d, want %d", got[0].Size, len(first))
	}
}

func TestEachTrustsDiskWhenTheIndexDisagrees(t *testing.T) {
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	t.Run("a record the index never mentioned is still read", func(t *testing.T) {
		dir := t.TempDir()
		s, err := spool.Create(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		writeRawcall(t, s, "req-indexed", "hash-a", day)
		unindexed := filepath.Join(dir, "20260801", "req-orphan.json")
		if err := os.WriteFile(unindexed, []byte(`{"capture":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}

		got := collect(t, s)
		if len(got) != 2 {
			t.Fatalf("Each visited %d rawcalls, want both", len(got))
		}
		orphan := got[1]
		if orphan.RequestID != "req-orphan" {
			t.Fatalf("second rawcall = %q", orphan.RequestID)
		}
		if orphan.SessionKey != "" {
			t.Errorf("SessionKey = %q, want empty for an unindexed record", orphan.SessionKey)
		}
		if orphan.Timestamp.IsZero() {
			t.Error("an unindexed record got no timestamp at all")
		}
	})

	t.Run("an index entry with no file on disk is ignored", func(t *testing.T) {
		dir := t.TempDir()
		s, err := spool.Create(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		writeRawcall(t, s, "req-a1", "hash-a", day)
		writeRawcall(t, s, "req-gone", "hash-a", day)
		if err := os.Remove(filepath.Join(dir, "20260801", "req-gone.json")); err != nil {
			t.Fatal(err)
		}

		got := collect(t, s)
		if len(got) != 1 || got[0].RequestID != "req-a1" {
			t.Errorf("Each visited %+v, want only the record still on disk", got)
		}
	})

	t.Run("a lost index costs metadata, not records", func(t *testing.T) {
		dir := t.TempDir()
		s, err := spool.Create(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		writeRawcall(t, s, "req-a1", "hash-a", day)
		if err := os.Remove(filepath.Join(dir, "20260801", indexName)); err != nil {
			t.Fatal(err)
		}

		got := collect(t, s)
		if len(got) != 1 || got[0].RequestID != "req-a1" {
			t.Fatalf("Each visited %+v, want the record", got)
		}
		if got[0].SessionKey != "" || got[0].Timestamp.IsZero() {
			t.Errorf("rebuilt rawcall = %+v, want no session key and a timestamp from disk", got[0])
		}
	})
}

func TestEachIgnoresForeignFilesAndStopsOnVisitorError(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	writeRawcall(t, s, "req-a1", "hash-a", day)
	writeRawcall(t, s, "req-a2", "hash-a", day)
	dayDir := filepath.Join(dir, "20260801")
	if err := os.WriteFile(filepath.Join(dayDir, "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dayDir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}

	if got := collect(t, s); len(got) != 2 {
		t.Errorf("Each visited %d rawcalls, want only the two rawcall files", len(got))
	}

	stop := os.ErrClosed
	visited := 0
	err = s.Each(func(spool.Rawcall) error {
		visited++
		return stop
	})
	if err != stop || visited != 1 {
		t.Errorf("Each = %v after %d visits, want the visitor error immediately", err, visited)
	}
}

func TestOldestReportsTheEarliestCaptureWithoutReadingData(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Oldest(); ok {
		t.Fatal("an empty spool reported an oldest rawcall")
	}
	early := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	late := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	writeRawcall(t, s, "req-late", "hash-a", late)
	writeRawcall(t, s, "req-early", "hash-a", early)

	oldest, ok := s.Oldest()
	if !ok || !oldest.Equal(early) {
		t.Fatalf("Oldest = %v, %v, want %v", oldest, ok, early)
	}
}

func TestOldestFallsBackToFileTimeWhenTheIndexIsGone(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	writeRawcall(t, s, "req-1", "hash-a", day)
	if err := os.Remove(filepath.Join(dir, "20260801", "index.jsonl")); err != nil {
		t.Fatal(err)
	}
	oldest, ok := s.Oldest()
	if !ok || oldest.IsZero() {
		t.Fatalf("Oldest = %v, %v, want a file-time fallback", oldest, ok)
	}
}
