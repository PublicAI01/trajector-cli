package gitsnapshot_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/gitsnapshot"
)

// repo is a git repository of the test's own, built with the git this
// machine has. Nothing here touches a repository the developer owns:
// the directory, the identity, and the configuration all live in the
// test's temp tree.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on this machine, so there is nothing to observe with")
	}
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "--initial-branch=main")
	r.git("config", "user.name", "Test")
	r.git("config", "user.email", "test@example.invalid")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes the named files and commits them, returning the commit.
func (r *repo) commit(message string, files map[string]string) string {
	r.t.Helper()
	for name, content := range files {
		path := filepath.Join(r.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "-A")
	r.git("commit", "-m", message)
	return r.git("rev-parse", "HEAD")
}

func (r *repo) observer() gitsnapshot.Observer {
	return gitsnapshot.Observer{Dir: r.dir}
}

func TestPositionReadsHeadItsFirstParentAndItsBranch(t *testing.T) {
	r := newRepo(t)
	root := r.commit("first", map[string]string{"a.txt": "one"})
	second := r.commit("second", map[string]string{"a.txt": "two"})

	got, err := r.observer().Position(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Head != second || got.Parent != root || got.Branch != "main" {
		t.Errorf("position = %+v, want head %s parent %s on main", got, second, root)
	}
}

func TestPositionReportsARootCommitAsHavingNoParent(t *testing.T) {
	r := newRepo(t)
	root := r.commit("first", map[string]string{"a.txt": "one"})

	got, err := r.observer().Position(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Head != root || got.Parent != "" {
		t.Errorf("position = %+v, want head %s with no parent", got, root)
	}
}

func TestPositionReadsTheBranchGitPrintsForADetachedHead(t *testing.T) {
	r := newRepo(t)
	root := r.commit("first", map[string]string{"a.txt": "one"})
	r.commit("second", map[string]string{"a.txt": "two"})
	r.git("checkout", "--detach", root)

	got, err := r.observer().Position(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "HEAD" {
		t.Errorf("branch = %q, want what git prints for a detached head", got.Branch)
	}
}

func TestPositionReportsNothingToObserve(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{
			name:  "a directory that is not inside a repository",
			setup: func(t *testing.T) string { return t.TempDir() },
		},
		{
			name:  "a repository with no commit yet",
			setup: func(t *testing.T) string { return newRepo(t).dir },
		},
		{
			name:  "a directory that does not exist",
			setup: func(t *testing.T) string { return filepath.Join(t.TempDir(), "gone") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := gitsnapshot.Observer{Dir: tt.setup(t)}
			if _, err := o.Position(t.Context()); err == nil {
				t.Error("a directory with nothing to observe reported a position")
			}
		})
	}
}

func TestChangesCopyEveryFieldGitPrints(t *testing.T) {
	r := newRepo(t)
	base := r.commit("first", map[string]string{"keep.txt": "one", "gone.txt": "bye"})
	if err := os.Remove(filepath.Join(r.dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	head := r.commit("second", map[string]string{"keep.txt": "two", "docs/new.md": "hi"})

	changes, err := r.observer().Changes(t.Context(), base, head)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]string{}
	for _, c := range changes {
		byPath[c.Path] = c.Status
		if len(c.OldBlob) != 40 || len(c.NewBlob) != 40 {
			t.Errorf("%s carries blobs %q/%q, want both spelled in full", c.Path, c.OldBlob, c.NewBlob)
		}
	}
	want := map[string]string{"keep.txt": "M", "gone.txt": "D", "docs/new.md": "A"}
	for path, status := range want {
		if byPath[path] != status {
			t.Errorf("%s = %q, want %q (all: %+v)", path, byPath[path], status, changes)
		}
	}
	if len(changes) != len(want) {
		t.Errorf("changes = %+v, want %d of them", changes, len(want))
	}
}

func TestChangesKeepGitsSpellingOfAnAbsentSide(t *testing.T) {
	r := newRepo(t)
	base := r.commit("first", map[string]string{"a.txt": "one"})
	head := r.commit("second", map[string]string{"b.txt": "two"})

	changes, err := r.observer().Changes(t.Context(), base, head)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Status == "A" && c.OldBlob != strings.Repeat("0", 40) {
			t.Errorf("an added path carries old blob %q, want the forty zeros git printed", c.OldBlob)
		}
	}
	if len(changes) == 0 {
		t.Fatal("two unrelated files changed and nothing was observed")
	}
}

func TestChangesDoNotAskForRenameDetection(t *testing.T) {
	r := newRepo(t)
	base := r.commit("first", map[string]string{"old.txt": "the same content on both sides"})
	r.git("mv", "old.txt", "new.txt")
	r.git("commit", "-m", "rename")
	head := r.git("rev-parse", "HEAD")

	changes, err := r.observer().Changes(t.Context(), base, head)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, c := range changes {
		statuses[c.Path] = c.Status
	}
	if statuses["old.txt"] != "D" || statuses["new.txt"] != "A" {
		t.Errorf("a rename was observed as %+v, want it stated as a deletion and an addition", changes)
	}
}

func TestChangesRefuseAnythingButACommitIdentifier(t *testing.T) {
	r := newRepo(t)
	head := r.commit("first", map[string]string{"a.txt": "one"})

	for _, base := range []string{"", "HEAD", "--output=/tmp/written", strings.Repeat("0", 39)} {
		if _, err := r.observer().Changes(t.Context(), base, head); err == nil {
			t.Errorf("Changes accepted %q as a commit", base)
		}
	}
}

func TestObservingIsBoundedByItsDeadline(t *testing.T) {
	r := newRepo(t)
	r.commit("first", map[string]string{"a.txt": "one"})

	o := gitsnapshot.Observer{Dir: r.dir, Timeout: time.Nanosecond}
	if _, err := o.Position(t.Context()); err == nil {
		t.Error("a deadline that cannot be met still reported a position")
	}
}

func TestValidCommitID(t *testing.T) {
	tests := map[string]bool{
		strings.Repeat("a", 40):       true,
		strings.Repeat("0", 40):       true,
		strings.Repeat("A", 40):       false,
		strings.Repeat("a", 39):       false,
		strings.Repeat("a", 41):       false,
		"":                            false,
		"-" + strings.Repeat("a", 39): false,
	}
	for id, want := range tests {
		if got := gitsnapshot.ValidCommitID(id); got != want {
			t.Errorf("ValidCommitID(%q) = %v, want %v", id, got, want)
		}
	}
}
