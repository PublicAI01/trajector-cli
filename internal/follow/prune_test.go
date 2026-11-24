package follow_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/harness/registryv1"
)

// noProcess answers that no process runs, so only a hook's recent
// event keeps a session hot.
func noProcess(int) bool { return false }

// longAgo is when a hook named a session that is cold now.
var longAgo = time.Now().Add(-72 * time.Hour)

func prune(t *testing.T, r *follow.Registry) int {
	t.Helper()
	removed, err := r.Prune(project, time.Now(), noProcess)
	if err != nil {
		t.Fatalf("Prune() = %v", err)
	}
	return removed
}

func TestPrune_RemovesOnlyTheColdEntriesWhoseFileIsGoneFromADirectoryStillThere(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	sessions := t.TempDir()
	at := func(name string) string { return filepath.Join(sessions, name) }
	kept, gone, outside := at("kept.jsonl"), at("gone.jsonl"), at("outside.jsonl")
	unmounted := filepath.Join(sessions, "unmounted", "u.jsonl")
	hot, later, broken := at("hot.jsonl"), at("later.jsonl"), at("broken.jsonl")
	writeFile(t, kept, userLine(1))
	mustRegister(t, r, project, kept, gone, outside, unmounted, hot, later, broken)
	if err := r.Warm(project, gone, 0, longAgo); err != nil {
		t.Fatal(err)
	}
	if err := r.Warm(project, hot, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	setKey(t, entryFileOf(dir, outside), "outside", "true")
	setKey(t, entryFileOf(dir, later), "needs", `["a-later-build"]`)
	writeFile(t, entryFileOf(dir, broken), `{"path":`)
	untouched := map[string]snapshot{}
	for _, path := range []string{kept, unmounted, hot, later, broken} {
		untouched[path] = snap(t, entryFileOf(dir, path))
	}

	removed := prune(t, r)

	if removed != 2 {
		t.Errorf("Prune() removed %d entries, want 2: the cold one and the one outside", removed)
	}
	want := slices.Sorted(slices.Values([]string{kept, unmounted, hot, later}))
	if paths, err := registeredPaths(r, project); err != nil || !slices.Equal(paths, want) {
		t.Errorf("Contents().Paths = %q, %v\nwant %q", paths, err, want)
	}
	for path, s := range untouched {
		if !s.same(t, entryFileOf(dir, path)) {
			t.Errorf("entry file of %s was rewritten, want it left as it was", filepath.Base(path))
		}
	}
	if n, err := r.Unreadable(project); err != nil || n != 1 {
		t.Errorf("Unreadable() = %d, %v, want the entry file that does not parse still there", n, err)
	}
}

func TestPrune_DropsTheHeatAndTheToldMarkWithTheLastEntryOfASession(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	sessions := t.TempDir()
	done, doneAgent := filepath.Join(sessions, "done.jsonl"), agentOf(filepath.Join(sessions, "done.jsonl"), "agent-d.jsonl")
	left, leftAgent := filepath.Join(sessions, "left.jsonl"), agentOf(filepath.Join(sessions, "left.jsonl"), "agent-l.jsonl")
	writeFile(t, doneAgent, userLine(1))
	writeFile(t, leftAgent, userLine(1))
	mustRegister(t, r, project, done, doneAgent, left, leftAgent)
	for _, main := range []string{done, left} {
		if err := r.Warm(project, main, 0, longAgo); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Tell(project, main); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(doneAgent); err != nil {
		t.Fatal(err)
	}

	if removed := prune(t, r); removed != 3 {
		t.Errorf("Prune() removed %d entries, want 3", removed)
	}

	for _, mark := range []string{".heat", ".told"} {
		if _, err := os.Stat(sessionFileOf(dir, done, mark)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s of the session whose every entry was removed: stat = %v, want it gone", mark, err)
		}
		if _, err := os.Stat(sessionFileOf(dir, left, mark)); err != nil {
			t.Errorf("%s of the session whose agent entry stays: stat = %v, want it there", mark, err)
		}
	}
	if paths, err := registeredPaths(r, project); err != nil || !slices.Equal(paths, []string{leftAgent}) {
		t.Errorf("Contents().Paths = %q, %v, want only the agent file that is there", paths, err)
	}
}

func TestPrune_WritesTheProjectFileOnceAndKeepsTheSumOfTheSignals(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	sessions := t.TempDir()
	var paths []string
	for i := range 12 {
		path := filepath.Join(sessions, string(rune('a'+i))+".jsonl")
		if i%3 == 0 {
			writeFile(t, path, userLine(1))
		}
		paths = append(paths, path)
	}
	mustRegister(t, r, project, paths...)
	for _, path := range paths {
		setKey(t, entryFileOf(dir, path), "signals", `{"assistant_lines":3,"assistant_lines_without_message_id":1}`)
	}
	before, err := r.Signals(project)
	if err != nil {
		t.Fatal(err)
	}
	writes := registryWrites(t, projectFileOf(dir))

	if removed := prune(t, r); removed != 8 {
		t.Errorf("Prune() removed %d entries, want 8", removed)
	}

	if n, counted := writes(); counted && n != 1 {
		t.Errorf("project file written %d times for 8 entries removed, want once", n)
	}
	if after, err := r.Signals(project); err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("Signals() = %+v, %v, want the sum before: %+v", after, err, before)
	}
	if removed := prune(t, r); removed != 0 {
		t.Errorf("Prune() again removed %d entries, want none", removed)
	}
	if n, counted := writes(); counted && n != 1 {
		t.Errorf("project file written %d times after a prune that removed nothing, want still once", n)
	}
}

func TestPrune_LeavesAnEntryAReaderHoldsToTheNextPrune(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	gone := filepath.Join(t.TempDir(), "gone.jsonl")
	mustRegister(t, r, project, gone)
	release, err := fsatomic.Lock(filepath.Join(projectDirOf(dir), digestOf(gone)), time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}

	if removed := prune(t, r); removed != 0 {
		t.Errorf("Prune() removed %d entries while a reader held the entry, want none", removed)
	}
	release()
	if removed := prune(t, r); removed != 1 {
		t.Errorf("Prune() once the reader let go removed %d entries, want 1", removed)
	}
}

func TestPrune_JudgesAnEntryAgainOnceItHoldsItsLock(t *testing.T) {
	for _, tc := range []struct {
		name     string
		meantime func(t *testing.T, r *follow.Registry, dir, path string)
	}{
		{"the file was written back", func(t *testing.T, _ *follow.Registry, _, path string) {
			t.Helper()
			writeFile(t, path, userLine(1))
		}},
		{"a hook named the session", func(t *testing.T, r *follow.Registry, _, path string) {
			t.Helper()
			if err := r.Warm(project, path, 0, time.Now()); err != nil {
				t.Fatal(err)
			}
		}},
		{"a later build took the entry", func(t *testing.T, _ *follow.Registry, dir, path string) {
			t.Helper()
			setKey(t, entryFileOf(dir, path), "needs", `["a-later-build"]`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r := follow.Open(dir)
			gone := filepath.Join(t.TempDir(), "gone.jsonl")
			mustRegister(t, r, project, gone)
			follow.BeforePruneLocks(t, func(path string) { tc.meantime(t, r, dir, path) })

			if removed := prune(t, r); removed != 0 {
				t.Errorf("Prune() removed %d entries, want none", removed)
			}
			if paths, err := registeredPaths(r, project); err != nil || !slices.Equal(paths, []string{gone}) {
				t.Errorf("Contents().Paths = %q, %v, want the entry kept", paths, err)
			}
		})
	}
}

func TestPrune_KeepsEveryEntryOfALargeRegistryWhoseFileIsThere(t *testing.T) {
	n := largeRegistryEntries()
	dir := filepath.Join(t.TempDir(), "follow")
	reg, kept, gone := largeRegistryWithFilesGone(t, n)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registryv1.Write(dir, project, reg); err != nil {
		t.Fatal(err)
	}
	r := follow.Open(dir)
	if err := r.Migrate(project); err != nil {
		t.Fatal(err)
	}
	removeAll(t, gone)

	start := time.Now()
	removed := prune(t, r)
	t.Logf("pruned %d of %d entries in %v", removed, n, time.Since(start))

	if removed != len(gone) {
		t.Errorf("Prune() removed %d entries, want %d", removed, len(gone))
	}
	if paths, err := registeredPaths(r, project); err != nil || !slices.Equal(paths, kept) {
		t.Errorf("Contents().Paths lists %d entries, %v, want the %d whose file is there", len(paths), err, len(kept))
	}
}

func TestPrune_LeavesARegistryNotMigratedYetAndOneThatIsNotThere(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(t.TempDir(), "gone.jsonl")
	if err := registryv1.Write(dir, project, registryv1.Registry{Files: []registryv1.Entry{{Path: gone}}}); err != nil {
		t.Fatal(err)
	}
	before := tree(t, dir)
	r := follow.Open(dir)

	if _, err := r.Prune(project, time.Now(), noProcess); !errors.Is(err, follow.ErrMigrationPending) {
		t.Errorf("Prune() of an earlier registry = %v, want ErrMigrationPending", err)
	}
	if diff := treeDiff(tree(t, dir), before); diff != "" {
		t.Errorf("Prune() changed an earlier registry:\n%s", diff)
	}
	if removed, err := r.Prune("ffff0000", time.Now(), noProcess); err != nil || removed != 0 {
		t.Errorf("Prune() of a project with no registry = %d, %v, want nothing removed", removed, err)
	}
}

// sessionsWithAgentFiles registers, in r, two sessions with an agent
// file and an agent file of a workflow run each: one whose session
// directory was removed with the session from a directory still there,
// and one in a directory that is not there. It returns the paths of
// the second session.
func sessionsWithAgentFiles(t *testing.T) (removed, unmounted []string) {
	t.Helper()
	sessions := t.TempDir()
	of := func(main string) []string {
		return []string{
			main,
			agentOf(main, "agent-a.jsonl"),
			filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", "workflows", "wf_1", "agent-w.jsonl"),
		}
	}
	removed = of(filepath.Join(sessions, "removed.jsonl"))
	for _, path := range removed {
		writeFile(t, path, userLine(1))
	}
	if err := os.RemoveAll(strings.TrimSuffix(removed[0], ".jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(removed[0]); err != nil {
		t.Fatal(err)
	}
	unmounted = of(filepath.Join(sessions, "unmounted", "u.jsonl"))
	slices.Sort(unmounted)
	return removed, unmounted
}

func TestPrune_RemovesTheAgentEntriesOfASessionRemovedWithItsDirectory(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	removed, unmounted := sessionsWithAgentFiles(t)
	mustRegister(t, r, project, slices.Concat(removed, unmounted)...)

	if n := prune(t, r); n != len(removed) {
		t.Errorf("Prune() removed %d entries, want the %d of the session removed", n, len(removed))
	}
	if paths, err := registeredPaths(r, project); err != nil || !slices.Equal(paths, unmounted) {
		t.Errorf("Contents().Paths = %q, %v\nwant the entries of the session whose main file's directory is not there: %q", paths, err, unmounted)
	}
}

func TestPrune_KeepsTheEntriesOfASessionWhoseProcessStillRuns(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	path := filepath.Join(t.TempDir(), "running.jsonl")
	mustRegister(t, r, project, path)
	if err := r.Warm(project, path, 4242, longAgo); err != nil {
		t.Fatal(err)
	}
	before := snap(t, entryFileOf(dir, path))

	removed, err := r.Prune(project, time.Now(), func(pid int) bool { return pid == 4242 })

	if err != nil || removed != 0 {
		t.Errorf("Prune() = %d, %v, want nothing removed while the session's process runs", removed, err)
	}
	if !before.same(t, entryFileOf(dir, path)) {
		t.Error("the entry was removed or rewritten, want it left to the session's reader")
	}
}
