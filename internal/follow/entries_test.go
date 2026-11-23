package follow_test

import (
	"os"
	"slices"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/follow"
)

func TestRegistry_CountsTheEntryFilesThatCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	broken, other := abs(t, "broken.jsonl"), abs(t, "other.jsonl")
	mustRegister(t, r, project, broken, other)
	if n, err := r.Unreadable(project); err != nil || n != 0 {
		t.Fatalf("Unreadable() = %d, %v, want 0 before any entry file breaks", n, err)
	}
	if err := os.WriteFile(entryFileOf(dir, broken), []byte(`{"path":`), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := r.Unreadable(project)

	if err != nil || n != 1 {
		t.Errorf("Unreadable() = %d, %v, want the one entry file that does not parse", n, err)
	}
	if files, err := r.Files(project); err != nil || len(files) != 1 || files[0].Path != other {
		t.Errorf("Files() = %+v, %v, want the other entry still listed", files, err)
	}
	if n, err := r.Unreadable("ffff0000"); err != nil || n != 0 {
		t.Errorf("Unreadable() of a project with no registry = %d, %v, want 0", n, err)
	}
}

func TestRegistry_PathsCountAnEntryThatNeedsWhatThisBuildDoesNotKnowAsRegistered(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	read, needs := abs(t, "read.jsonl"), abs(t, "needs.jsonl")
	mustRegister(t, r, project, read, needs)
	setKey(t, entryFileOf(dir, needs), "needs", `["a-later-build"]`)

	paths, err := r.Paths(project)

	if want := slices.Sorted(slices.Values([]string{read, needs})); err != nil || !slices.Equal(paths, want) {
		t.Errorf("Paths() = %q, %v, want %q: the entry a later build needs is registered", paths, err, want)
	}
	if files, err := r.Files(project); err != nil || len(files) != 1 || files[0].Path != read {
		t.Errorf("Files() = %+v, %v, want only the entry this build reads", files, err)
	}
}
