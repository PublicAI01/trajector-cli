package spool_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/spool"
)

func TestWritableFailsOnUnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not block writes on windows")
	}
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if err := s.Writable(); err == nil {
		t.Error("Writable succeeded on a read-only directory")
	}
}

func TestDeleteWhereSkipsUnparsableIndexLines(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	writeRawcall(t, s, "req-a1", "hash-a", day)
	indexPath := filepath.Join(dir, "20260801", indexName)
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := append([]byte("not-json\n"), index...)
	if err := os.WriteFile(indexPath, corrupted, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.DeleteWhere(matchProject("hash-a")); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "not-json\n" {
		t.Errorf("index after delete = %q, want the unparsable line kept", after)
	}
}

func TestDeleteWhereIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	writeRawcall(t, s, "req-a1", "hash-a", day)
	dayDir := filepath.Join(dir, "20260801")
	if err := os.WriteFile(filepath.Join(dayDir, "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dayDir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.DeleteWhere(func(spool.Rawcall) bool { return true })
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteWhere = %d, %v", deleted, err)
	}
	if _, err := os.Stat(filepath.Join(dayDir, "notes.txt")); err != nil {
		t.Error("non-rawcall file was touched")
	}
}

func TestDeleteWhereWithoutIndexStillDeletes(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	writeRawcall(t, s, "req-a1", "hash-a", day)
	if err := os.Remove(filepath.Join(dir, "20260801", indexName)); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.DeleteWhere(matchProject("hash-a"))
	if err != nil || deleted != 1 {
		t.Errorf("DeleteWhere = %d, %v", deleted, err)
	}
}

func TestDeleteWhereOnVanishedSpoolDirDeletesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteWhere(func(spool.Rawcall) bool { return true })
	if err != nil || deleted != 0 {
		t.Errorf("DeleteWhere = %d, %v, want 0, nil: a spool holding nothing has nothing to withdraw", deleted, err)
	}
}
