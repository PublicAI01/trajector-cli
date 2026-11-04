package discover

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestWalkNativeVolumeFindsOnlyTheEnabledProjectsSessions(t *testing.T) {
	tr := tree{root: canonicalTempDir(t), configDir: t.TempDir()}
	main := tr.session(t, tr.root, "root", time.Now())
	child := tr.session(t, tr.mkdir(t, "child"), "child", time.Now())
	tr.session(t, canonicalTempDir(t), "unrelated", time.Now())

	res := mustWalk(t, tr)
	want := []string{main, child}
	slices.Sort(want)
	slices.Sort(res.Sessions)
	if !slices.Equal(res.Sessions, want) || res.Gaps.Any() {
		t.Fatalf("Walk = %+v; want only %v with no gaps", res, want)
	}
}

func TestWalkNativeVolumeStillRejectsAmbiguousNames(t *testing.T) {
	tr := tree{root: canonicalTempDir(t), configDir: t.TempDir()}
	joined := tr.mkdir(t, "a-b")
	nested := tr.mkdir(t, filepath.Join("a", "b"))
	tr.session(t, joined, "ambiguous", time.Now())
	if Encode(joined) != Encode(nested) {
		t.Fatal("fixture paths must encode alike")
	}
	res := mustWalk(t, tr)
	if len(res.Sessions) != 0 || len(res.Gaps.Ambiguous) != 2 {
		t.Fatalf("Walk = %+v; want both ambiguous directories skipped", res)
	}
	for _, gap := range res.Gaps.Ambiguous {
		if !slices.Equal(gap.Matches, []string{joined, nested}) {
			t.Fatalf("Matches = %v; want both real paths", gap.Matches)
		}
	}
}
