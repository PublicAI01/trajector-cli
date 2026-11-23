package follow_test

import (
	"slices"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/follow"
)

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
