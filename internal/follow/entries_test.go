package follow_test

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"slices"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/harness/registryv1"
)

// unreadable is what Contents counts of the entry files of project's
// registry that cannot be read.
func unreadable(r *follow.Registry, project string) (int, error) {
	c, err := r.Contents(project)
	return c.Unreadable, err
}

// registeredPaths is what Contents states about the paths project's
// registry holds.
func registeredPaths(r *follow.Registry, project string) ([]string, error) {
	c, err := r.Contents(project)
	return c.Paths, err
}

func TestRegistry_CountsTheEntryFilesThatCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	broken, other := abs(t, "broken.jsonl"), abs(t, "other.jsonl")
	mustRegister(t, r, project, broken, other)
	if n, err := unreadable(r, project); err != nil || n != 0 {
		t.Fatalf("Contents().Unreadable = %d, %v, want 0 before any entry file breaks", n, err)
	}
	if err := os.WriteFile(entryFileOf(dir, broken), []byte(`{"path":`), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := unreadable(r, project)

	if err != nil || n != 1 {
		t.Errorf("Contents().Unreadable = %d, %v, want the one entry file that does not parse", n, err)
	}
	if files, err := r.Files(project); err != nil || len(files) != 1 || files[0].Path != other {
		t.Errorf("Files() = %+v, %v, want the other entry still listed", files, err)
	}
	if n, err := unreadable(r, "ffff0000"); err != nil || n != 0 {
		t.Errorf("Contents().Unreadable of a project with no registry = %d, %v, want 0", n, err)
	}
}

func TestRegistry_ContentsCountAnEntryThatNeedsWhatThisBuildDoesNotKnowAsRegistered(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	read, needs := abs(t, "read.jsonl"), abs(t, "needs.jsonl")
	mustRegister(t, r, project, read, needs)
	setKey(t, entryFileOf(dir, needs), "needs", `["a-later-build"]`)

	paths, err := registeredPaths(r, project)

	if want := slices.Sorted(slices.Values([]string{read, needs})); err != nil || !slices.Equal(paths, want) {
		t.Errorf("Contents().Paths = %q, %v, want %q: the entry a later build needs is registered", paths, err, want)
	}
	if files, err := r.Files(project); err != nil || len(files) != 1 || files[0].Path != read {
		t.Errorf("Files() = %+v, %v, want only the entry this build reads", files, err)
	}
}

func TestRegistry_TakesNewEntriesPlacesOneAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	mustRegister(t, r, project, abs(t, "one.jsonl"))
	before := namesIn(t, projectDirOf(dir))

	asked, refused := r.TakesNewEntries(project)

	if refused != nil || !asked {
		t.Errorf("TakesNewEntries() = %v, %v, want the entry taken", asked, refused)
	}
	if after := namesIn(t, projectDirOf(dir)); !slices.Equal(after, before) {
		t.Errorf("project directory = %v after the check, want %v", after, before)
	}
}

func TestRegistry_TakesNewEntriesRefusesWithWhatTheSystemSaidAsARegistrationDoes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not block writes on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes into every directory")
	}
	dir := t.TempDir()
	r := follow.Open(dir)
	mustRegister(t, r, project, abs(t, "one.jsonl"))
	before := namesIn(t, projectDirOf(dir))
	if err := os.Chmod(projectDirOf(dir), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(projectDirOf(dir), 0o700) })

	asked, refused := r.TakesNewEntries(project)

	if !errors.Is(refused, fs.ErrPermission) || !asked {
		t.Errorf("TakesNewEntries() = %v, %v, want the entry refused for permission", asked, refused)
	}
	if err := r.RegisterAll(project, []string{abs(t, "two.jsonl")}, ""); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("RegisterAll() = %v, want it refused for the same reason", err)
	}
	if after := namesIn(t, projectDirOf(dir)); !slices.Equal(after, before) {
		t.Errorf("project directory = %v after the check, want %v", after, before)
	}
}

func TestRegistry_TakesNewEntriesDoesNotAskAProjectWithoutASettledRegistry(t *testing.T) {
	dir := t.TempDir()
	r := follow.Open(dir)
	if asked, refused := r.TakesNewEntries(project); refused != nil || asked {
		t.Errorf("TakesNewEntries() of a project with no registry = %v, %v, want it not asked", asked, refused)
	}
	if err := registryv1.LeaveHalfway(dir, project, registryv1.Registry{Files: []registryv1.Entry{
		{Path: abs(t, "one.jsonl"), Offset: 11},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Files(project); !errors.Is(err, follow.ErrMigrationPending) {
		t.Fatalf("Files() = %v, want the project waiting for its migration", err)
	}
	before := namesIn(t, projectDirOf(dir))

	asked, refused := r.TakesNewEntries(project)

	if refused != nil || asked {
		t.Errorf("TakesNewEntries() of a project waiting for its migration = %v, %v, want it not asked", asked, refused)
	}
	if after := namesIn(t, projectDirOf(dir)); !slices.Equal(after, before) {
		t.Errorf("project directory = %v, want %v", after, before)
	}
}

func namesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
