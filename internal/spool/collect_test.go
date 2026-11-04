package spool_test

import (
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

func TestCollectBoundsEitherSourceAndFillsFromTheOther(t *testing.T) {
	for _, recordsFirst := range []bool{false, true} {
		name := "rawcalls-first"
		if recordsFirst {
			name = "records-first"
		}
		t.Run(name, func(t *testing.T) {
			s, err := spool.Create(t.TempDir(), 0)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
			if err := s.Write(rawcallOfSession(t, "req-1", sessionA, "hash-p1", at)); err != nil {
				t.Fatal(err)
			}
			if err := s.WriteSegment(segment(sessionA, "hash-p1", 0, at)); err != nil {
				t.Fatal(err)
			}
			entries, err := s.Collect(1, recordsFirst)
			if err != nil || len(entries) != 1 {
				t.Fatalf("bounded collect returned %d entries, %v", len(entries), err)
			}
			want := envelope.KindRawcall
			if recordsFirst {
				want = envelope.KindSegment
			}
			if entries[0].Kind != want {
				t.Fatalf("first kind = %+v, want %+v", entries[0].Kind, want)
			}
			all, err := s.Collect(1<<20, recordsFirst)
			if err != nil || len(all) != 2 || all[0].Kind != want {
				t.Fatalf("combined collect returned %d entries, %v", len(all), err)
			}
			if err := s.DeleteEntries(entries); err != nil {
				t.Fatal(err)
			}
			remaining, err := s.Collect(1, recordsFirst)
			if err != nil || len(remaining) != 1 || remaining[0].Kind == want {
				t.Fatalf("collect with an empty preferred slot returned %d entries, %v", len(remaining), err)
			}
			if err := s.DeleteEntries(remaining); err != nil {
				t.Fatal(err)
			}
			empty, err := s.Collect(1, recordsFirst)
			if err != nil || len(empty) != 0 {
				t.Fatalf("empty collect returned %d entries, %v", len(empty), err)
			}
		})
	}
}
