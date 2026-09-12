package drift_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/drift"
)

func TestLogKeepsEveryEntryInTheOrderItWasAppended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "reader.log")
	alert := drift.Signals{AssistantLines: 4, AssistantLinesWithoutMessageID: 1}
	stopped := drift.Signals{UnanchoredPathFields: []string{"$.someNewPath"}}
	if err := drift.AppendLog(path, "2026-09-11T08:00:00Z", "hash-a", alert); err != nil {
		t.Fatal(err)
	}
	if err := drift.AppendLog(path, "2026-09-11T08:00:01Z", "hash-b", stopped); err != nil {
		t.Fatal(err)
	}

	entries, err := drift.ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want both appends", len(entries))
	}
	first, second := entries[0], entries[1]
	if first.At != "2026-09-11T08:00:00Z" || first.ProjectIDHash != "hash-a" || first.Stop {
		t.Errorf("first entry = %+v, want the alert of hash-a", first)
	}
	if first.AssistantLinesWithoutMessageID != 1 || first.AssistantLines != 4 {
		t.Errorf("first entry = %+v, want the counts it was appended with", first)
	}
	if second.ProjectIDHash != "hash-b" || !second.Stop {
		t.Errorf("second entry = %+v, want the stop of hash-b", second)
	}
	if strings.Join(second.UnanchoredPathFields, ",") != "$.someNewPath" {
		t.Errorf("second entry = %+v, want the field name it was appended with", second)
	}
}

func TestLogAndItsDirectoryAreOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "reader.log")
	if err := drift.AppendLog(path, "2026-09-11T08:00:00Z", "hash-a", drift.Signals{AgentLines: 1, AgentLinesWithoutParent: 1}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		mode os.FileMode
	}{{dir, 0o700}, {path, 0o600}} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != tc.mode {
			t.Errorf("%s mode = %v, want %v", tc.path, info.Mode().Perm(), tc.mode)
		}
	}
}

func TestLogThatCannotBeWrittenIsAnError(t *testing.T) {
	dir := t.TempDir()
	occupied := filepath.Join(dir, "occupied")
	if err := os.WriteFile(occupied, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
	}{
		{"the directory the log belongs in is a file", filepath.Join(occupied, "reader.log")},
		{"the log itself is a directory", dir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := drift.AppendLog(tc.path, "2026-09-11T08:00:00Z", "hash-a", drift.Signals{AgentLines: 1, AgentLinesWithoutParent: 1}); err == nil {
				t.Error("AppendLog = nil, want the failure reported to the caller")
			}
		})
	}
}

func TestLogNothingWroteHasNoEntries(t *testing.T) {
	entries, err := drift.ReadLog(filepath.Join(t.TempDir(), "reader.log"))
	if err != nil || len(entries) != 0 {
		t.Errorf("ReadLog = %v, %v; want no entries and no error", entries, err)
	}
}

func TestLogRefusesALineItDidNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader.log")
	if err := os.WriteFile(path, []byte("not an entry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := drift.ReadLog(path); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("ReadLog = %v, want an error naming line 1", err)
	}
}

func TestLogPastItsSizeLimitKeepsItsNewestEntriesAndGrowsNoFurther(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "reader.log")
	newValue := strings.Repeat("mood-ring-", 40)
	found := drift.Signals{AssistantLines: 4, NewTopLevelTypes: []string{newValue}}
	appended := 0
	add := func() int64 {
		t.Helper()
		if err := drift.AppendLog(path, "2026-09-11T08:00:00Z", fmt.Sprintf("hash-%06d", appended), found); err != nil {
			t.Fatal(err)
		}
		appended++
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}

	entrySize := add()
	peak, size := entrySize, entrySize
	for {
		size = add()
		if size < peak {
			break
		}
		peak = size
		if appended > 20000 {
			t.Fatalf("log = %d bytes after %d entries, want it rewritten before it grows this far", size, appended)
		}
	}
	shrunkTo, grown := size, appended
	for i := 0; i < grown; i++ {
		if got := add(); got > peak+entrySize {
			t.Fatalf("log = %d bytes after %d entries, want it near the %d bytes it was rewritten at", got, appended, peak)
		}
	}

	entries, err := drift.ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("entries = %d, want a rewrite to keep a tail of them", len(entries))
	}
	last := entries[len(entries)-1]
	if last.ProjectIDHash != fmt.Sprintf("hash-%06d", appended-1) {
		t.Errorf("last entry = %+v, want the newest one appended", last)
	}
	if last.AssistantLines != 4 || strings.Join(last.NewTopLevelTypes, ",") != newValue {
		t.Errorf("last entry = %+v, want it whole", last)
	}
	if entries[0].ProjectIDHash == "hash-000000" {
		t.Errorf("first entry = %+v, want the oldest entries dropped", entries[0])
	}
	if shrunkTo >= peak {
		t.Errorf("log = %d bytes after the rewrite, want it below the %d bytes it reached", shrunkTo, peak)
	}
}
