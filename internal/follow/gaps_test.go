package follow_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/follow"
)

func TestRegistry_GapsAreKeptWithTheRegistry(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	gaps := follow.Gaps{
		Truncated:  true,
		Ambiguous:  []follow.Ambiguity{{Dir: abs(t, "p/a-b"), Name: "-p-a-b", Matches: []string{abs(t, "p/a-b"), abs(t, "p/a_b")}}},
		Unreadable: []string{abs(t, "p/locked")},
	}

	if err := r.SetGaps(project, gaps); err != nil {
		t.Fatal(err)
	}
	got, err := gapsOf(r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, gaps) {
		t.Errorf("Gaps = %+v, want %+v", got, gaps)
	}
	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("Files = %v, want a registry that holds gaps and no files", files)
	}
	raw, err := os.ReadFile(projectFileOf(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version":2`) || !strings.Contains(string(raw), `"gaps":{"truncated":true`) {
		t.Errorf("project file = %s, want version 2 with a gaps object", raw)
	}

	if err := r.SetGaps(project, follow.Gaps{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := gapsOf(r); got.Any() {
		t.Errorf("Gaps after a search that covered everything = %+v, want none", got)
	}
	raw, _ = os.ReadFile(projectFileOf(dir))
	if strings.Contains(string(raw), "gaps") {
		t.Errorf("project file = %s, want no gaps key once nothing is uncovered", raw)
	}
}

func TestRegistry_GapsOfAnUnknownProjectAreNone(t *testing.T) {
	got, err := gapsOf(follow.Open(t.TempDir()))
	if err != nil || got.Any() {
		t.Errorf("Gaps = %+v, %v; want none and no error", got, err)
	}
}

func TestRegistry_ACursorWriteKeepsWhenAFileWasRead(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	path := abs(t, "s.jsonl")
	if err := r.RegisterAll(project, []string{path}, ""); err != nil {
		t.Fatal(err)
	}
	if err := follow.MoveCursor(r, project, follow.File{Path: path}, follow.File{Path: path, Offset: 3, ReadAt: "2026-09-10T08:30:00Z"}); err != nil {
		t.Fatal(err)
	}

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if files[0].ReadAt != "2026-09-10T08:30:00Z" {
		t.Errorf("ReadAt = %q, want the time the cursor write carried", files[0].ReadAt)
	}
	if got := string(keysOf(t, entryFileOf(dir, path))["read_at"]); got != `"2026-09-10T08:30:00Z"` {
		t.Errorf("stored read_at = %s, want it written", got)
	}

	other := abs(t, "never-read.jsonl")
	if err := r.RegisterAll(project, []string{other}, ""); err != nil {
		t.Fatal(err)
	}
	if entry := keysOf(t, entryFileOf(dir, other)); entry["read_at"] != nil {
		t.Errorf("entry of a file never read = %v, want no read_at", entry)
	}
}
