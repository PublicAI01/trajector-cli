package spool_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// heldDir restates the documented on-disk home of the held slot.
const heldDir = "records-held"

func holdSegment(t *testing.T, sp *spool.Spool, seg envelope.Segment) {
	t.Helper()
	data, err := seg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.Hold(data); err != nil {
		t.Fatal(err)
	}
}

func TestHeldRecordsLiveInTheirOwnSlotAndReachNoBatch(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	seg := segment(sessionA, "hash-a", 0, at)
	holdSegment(t, sp, seg)
	holdSegment(t, sp, seg)

	held, err := sp.Held()
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].SessionID != sessionA {
		t.Fatalf("held = %+v, want the one segment, stored once for its id", held)
	}
	path := filepath.Join(dir, heldDir, at.Format("20060102"), held[0].ID+".json")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("held record is not at %s: %v", path, err)
	}
	var uploadable []spool.Record
	if err := sp.EachRecord(func(r spool.Record) error {
		uploadable = append(uploadable, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(uploadable) != 0 {
		t.Errorf("records = %+v, want a held record to reach no batch", uploadable)
	}
}

func TestReleaseMovesAHeldRecordIntoTheUploadSlot(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	seg := segment(sessionA, "hash-a", 0, at)
	holdSegment(t, sp, seg)

	if err := sp.Release(seg.RecordID); err != nil {
		t.Fatal(err)
	}

	held, err := sp.Held()
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Errorf("held = %+v, want the record released", held)
	}
	var uploadable []spool.Record
	if err := sp.EachRecord(func(r spool.Record) error {
		uploadable = append(uploadable, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(uploadable) != 1 || uploadable[0].ID != seg.RecordID {
		t.Errorf("records = %+v, want the released segment waiting for upload", uploadable)
	}
}

func TestDeletingASessionTakesItsHeldRecords(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	holdSegment(t, sp, segment(sessionA, "hash-a", 0, at))
	holdSegment(t, sp, segment(sessionB, "hash-a", 0, at))

	_, records, err := sp.DeleteSession(sessionA)
	if err != nil {
		t.Fatal(err)
	}
	if records != 1 {
		t.Errorf("deleted records = %d, want the held segment of the session counted", records)
	}
	held, err := sp.Held()
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].SessionID != sessionB {
		t.Errorf("held = %+v, want only the other session's segment left", held)
	}
}

func TestWithdrawalTakesAProjectsHeldRecords(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	holdSegment(t, sp, segment(sessionA, "hash-a", 0, at))
	holdSegment(t, sp, segment(sessionB, "hash-b", 0, at))

	deleted, err := sp.DeleteProject("hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want the project's held segment counted", deleted)
	}
	held, err := sp.Held()
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].ProjectIDHash != "hash-b" {
		t.Errorf("held = %+v, want only the other project's segment left", held)
	}
}

func TestHeldRecordsAreChargedToNoQuota(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	before := sp.Usage()
	holdSegment(t, sp, segment(sessionA, "hash-a", 0, at))
	if got := sp.Usage(); got != before {
		t.Errorf("Usage = %d, want it unchanged at %d while only the held slot grew", got, before)
	}
	if err := sp.Writable(); err != nil {
		t.Errorf("Writable = %v, want a spool holding only held records to accept writes", err)
	}

	if err := sp.Release(segment(sessionA, "hash-a", 0, at).RecordID); err != nil {
		t.Fatal(err)
	}
	if got := sp.Usage(); got <= before {
		t.Errorf("Usage after release = %d, want the record charged once it waits for upload", got)
	}
}

func TestHoldRefusesWhatCouldNotBeDeletedAfterwards(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.Create(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"a rawcall, which no record slot holds", storedRawcall(t, "msg_01", "", "hash-a", at).Bytes()},
		{"a record naming no session", segmentBytes(t, segment("", "hash-a", 0, at))},
		{"a record naming no capture time", segmentBytes(t, segment(sessionA, "hash-a", 0, time.Time{}))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := sp.Hold(tc.data); err == nil {
				t.Error("Hold = nil, want the record refused")
			}
			held, err := sp.Held()
			if err != nil || len(held) != 0 {
				t.Errorf("held = %+v, %v; want the slot left empty", held, err)
			}
		})
	}
}

// segmentBytes is the serialized form the reader hands to the spool.
func segmentBytes(t *testing.T, seg envelope.Segment) []byte {
	t.Helper()
	data, err := seg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}
