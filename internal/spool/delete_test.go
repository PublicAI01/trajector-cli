package spool_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

func writeRawcall(t *testing.T, s *spool.Spool, id, projectHash string, day time.Time) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"request_id": id,
		"capture":    map[string]any{"project_id_hash": projectHash},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(spool.Entry{RequestID: id, SessionKey: "sess", Timestamp: day}, data); err != nil {
		t.Fatal(err)
	}
	return data
}

func matchProject(hash string) func(spool.Rawcall) bool {
	return func(r spool.Rawcall) bool {
		stored, ok := envelope.ProjectIDHashOf(r.Data)
		return ok && stored == hash
	}
}

func TestDeleteWhereRemovesOnlyMatchingRawcalls(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day1 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	writeRawcall(t, s, "req-a1", "hash-a", day1)
	writeRawcall(t, s, "req-a2", "hash-a", day2)
	keep := writeRawcall(t, s, "req-b1", "hash-b", day1)

	deleted, err := s.DeleteWhere(matchProject("hash-a"))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}

	if _, err := os.Stat(filepath.Join(dir, "20260801", "req-a1.json")); !os.IsNotExist(err) {
		t.Error("req-a1 still present")
	}
	if _, err := os.Stat(filepath.Join(dir, "20260802", "req-a2.json")); !os.IsNotExist(err) {
		t.Error("req-a2 still present")
	}
	got, err := os.ReadFile(filepath.Join(dir, "20260801", "req-b1.json"))
	if err != nil || !bytes.Equal(got, keep) {
		t.Errorf("unrelated rawcall changed: %v", err)
	}

	index, err := os.ReadFile(filepath.Join(dir, "20260801", indexName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(index, []byte("req-a1")) {
		t.Error("index still lists the deleted rawcall")
	}
	if !bytes.Contains(index, []byte("req-b1")) {
		t.Error("index lost the surviving rawcall")
	}
}

func TestDeleteWhereFreesQuota(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	writeRawcall(t, s, "req-a1", "hash-a", day)
	before := s.Usage()
	if _, err := s.DeleteWhere(matchProject("hash-a")); err != nil {
		t.Fatal(err)
	}
	if s.Usage() >= before {
		t.Errorf("usage after delete = %d, want below %d", s.Usage(), before)
	}

	reopened, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Usage() != s.Usage() {
		t.Errorf("recomputed usage = %d, tracked usage = %d", reopened.Usage(), s.Usage())
	}
}

func TestForeignDeleteFreesQuotaForAnOpenHandle(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	seed, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	data := writeRawcall(t, seed, "req-a1", "hash-a", day)

	writer, err := spool.Open(dir, seed.Usage())
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(spool.Entry{RequestID: "req-a2", Timestamp: day}, data); !errors.Is(err, spool.ErrQuotaExceeded) {
		t.Fatalf("write on a full spool = %v, want ErrQuotaExceeded", err)
	}

	deleter, err := spool.Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deleter.DeleteWhere(matchProject("hash-a")); err != nil {
		t.Fatal(err)
	}

	if err := writer.Writable(); err != nil {
		t.Errorf("Writable after a foreign delete = %v, want nil", err)
	}
	if err := writer.Write(spool.Entry{RequestID: "req-a2", Timestamp: day}, data); err != nil {
		t.Errorf("Write after a foreign delete = %v, want success", err)
	}
	fresh, err := spool.Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if writer.Usage() != fresh.Usage() {
		t.Errorf("open handle usage = %d, freshly derived usage = %d", writer.Usage(), fresh.Usage())
	}
}

func TestForeignWriteCountsAgainstAnOpenHandleQuota(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	a, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := spool.Open(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeRawcall(t, b, "req-b1", "hash-b", day)
	if a.Usage() != b.Usage() {
		t.Errorf("handle A usage = %d after a foreign write, handle B usage = %d", a.Usage(), b.Usage())
	}
}

func TestDeleteWhereNoMatchesTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	writeRawcall(t, s, "req-b1", "hash-b", day)
	deleted, err := s.DeleteWhere(matchProject("hash-a"))
	if err != nil || deleted != 0 {
		t.Errorf("DeleteWhere = %d, %v", deleted, err)
	}
}

func TestWritableProbe(t *testing.T) {
	dir := t.TempDir()
	s, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Writable(); err != nil {
		t.Errorf("Writable on healthy spool = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left %d entries behind", len(entries))
	}

}

func TestWritableReportsExhaustedQuota(t *testing.T) {
	dir := t.TempDir()
	seed, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	writeRawcall(t, seed, "req-a1", "hash-a", day)

	full, err := spool.Create(dir, seed.Usage())
	if err != nil {
		t.Fatal(err)
	}
	if err := full.Writable(); !errors.Is(err, spool.ErrQuotaExceeded) {
		t.Errorf("Writable on full spool = %v, want quota error", err)
	}
}
