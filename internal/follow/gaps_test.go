package follow_test

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	got, err := r.Gaps(project)
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
	raw, err := os.ReadFile(filepath.Join(dir, project+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version":1`) || !strings.Contains(string(raw), `"gaps":{"truncated":true`) {
		t.Errorf("registry file = %s, want version 1 with a gaps object", raw)
	}

	if err := r.SetGaps(project, follow.Gaps{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Gaps(project); got.Any() {
		t.Errorf("Gaps after a search that covered everything = %+v, want none", got)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, project+".json"))
	if strings.Contains(string(raw), "gaps") {
		t.Errorf("registry file = %s, want no gaps key once nothing is uncovered", raw)
	}
}

func TestRegistry_GapsOfAnUnknownProjectAreNone(t *testing.T) {
	got, err := follow.Open(t.TempDir()).Gaps(project)
	if err != nil || got.Any() {
		t.Errorf("Gaps = %+v, %v; want none and no error", got, err)
	}
}

func TestRegistry_UpdateKeepsWhenAFileWasRead(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	path := abs(t, "s.jsonl")
	if err := r.Register(project, path); err != nil {
		t.Fatal(err)
	}
	if err := r.Update(project, follow.File{Path: path, Offset: 3, ReadAt: "2026-09-10T08:30:00Z"}); err != nil {
		t.Fatal(err)
	}

	files, err := r.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	if files[0].ReadAt != "2026-09-10T08:30:00Z" {
		t.Errorf("ReadAt = %q, want the time the update carried", files[0].ReadAt)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, project+".json"))
	var stored struct {
		Files []map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if string(stored.Files[0]["read_at"]) != `"2026-09-10T08:30:00Z"` {
		t.Errorf("stored entry = %s, want read_at written", raw)
	}

	other := abs(t, "never-read.jsonl")
	if err := r.Register(project, other); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, project+".json"))
	if strings.Count(string(raw), "read_at") != 1 {
		t.Errorf("registry file = %s, want no read_at on a file never read", raw)
	}
}
