package discover

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/follow"
)

func TestEncode(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"ascii", "/home/u/proj", "-home-u-proj"},
		{"dash dot underscore space", "/tmp/a.b_c d", "-tmp-a-b-c-d"},
		{"chinese one dash per code unit", "/home/用户/项目", "-home------"},
		{"decomposed accent is composed first", "/tmp/café", "-tmp-caf-"},
		{"composed accent", "/tmp/café", "-tmp-caf-"},
		{"surrogate pair is two dashes", "/tmp/\U0001F600", "-tmp---"},
		{"exactly 200 units keeps no hash", "/" + strings.Repeat("a", 199), "-" + strings.Repeat("a", 199)},
		// Hash values cross-checked with independent Python and
		// JavaScript implementations of the same definition.
		{"251 ascii units", "/" + strings.Repeat("a", 250), "-" + strings.Repeat("a", 199) + "-feo44x"},
		{"248 units of chinese", "/home/u/" + strings.Repeat("用户", 120), "-home-u-" + strings.Repeat("-", 192) + "-el0p6r"},
		{"201 units", "/tmp/x" + strings.Repeat("-", 195), "-tmp-x" + strings.Repeat("-", 194) + "-9zdanw"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Encode(c.path); got != c.want {
				t.Errorf("Encode(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

func TestEncode_HashUsesNormalizedInput(t *testing.T) {
	long := strings.Repeat("a", 250)
	if Encode("/café/"+long) != Encode("/café/"+long) {
		t.Error("decomposed and composed spellings of a long path must encode alike")
	}
}

type tree struct {
	root, configDir string
}

func newTree(t *testing.T) tree {
	t.Helper()
	return tree{root: canonicalTempDir(t), configDir: t.TempDir()}
}

// canonicalTempDir is a temporary directory in the spelling a walk is
// handed: the project root a machine passes is the consent root, which
// has had its symbolic links resolved, and the name Claude Code derives
// for the project is derived from that spelling. On macOS the temporary
// directory itself sits behind one (/var is /private/var), so a fixture
// built from the unresolved path would be named for a directory the walk
// never asks about.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func (tr tree) mkdir(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.Join(tr.root, rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// project creates the directory Claude Code would use for dir and
// returns it.
func (tr tree) project(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(tr.configDir, "projects", Encode(dir))
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func (tr tree) session(t *testing.T, dir, sid string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(tr.project(t, dir), sid+".jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustWalk(t *testing.T, tr tree) Result {
	t.Helper()
	res, err := Walk(tr.root, tr.configDir)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return res
}

func TestWalk_FindsSessionsUnderEveryDirectory(t *testing.T) {
	tr := newTree(t)
	day := 24 * time.Hour
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	rootSession := tr.session(t, tr.root, "root", t0.Add(5*day))
	sub := tr.mkdir(t, "pkg/with-dash.and.dot")
	subSession := tr.session(t, sub, "sub", t0)
	cjk := tr.mkdir(t, "文档/子目录")
	cjkSession := tr.session(t, cjk, "cjk", t0.Add(2*day))
	tr.mkdir(t, "no-sessions-here/deeper")
	tr.project(t, tr.root+"/never-created-on-disk")
	writeFile(t, filepath.Join(tr.project(t, sub), "notes.txt"))
	writeFile(t, filepath.Join(tr.project(t, sub), "memory", "MEMORY.md"))

	res := mustWalk(t, tr)

	want := Result{
		Files:    []string{rootSession, subSession, cjkSession},
		Sessions: []string{rootSession, subSession, cjkSession},
		Oldest:   t0,
		visited:  7,
	}
	slices.Sort(want.Files)
	slices.Sort(want.Sessions)
	slices.Sort(res.Files)
	slices.Sort(res.Sessions)
	if !res.Oldest.Equal(want.Oldest) {
		t.Errorf("Oldest = %v, want %v", res.Oldest, want.Oldest)
	}
	res.Oldest, want.Oldest = time.Time{}, time.Time{}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("Walk = %+v\nwant %+v", res, want)
	}
}

func TestWalk_DoesNotSkipDotGitOrNodeModules(t *testing.T) {
	tr := newTree(t)
	var want []string
	for _, rel := range []string{".git/hooks", "node_modules/left-pad", "target/debug", ".hidden"} {
		want = append(want, tr.session(t, tr.mkdir(t, rel), "s", time.Now()))
	}

	res := mustWalk(t, tr)

	slices.Sort(want)
	slices.Sort(res.Files)
	if !slices.Equal(res.Files, want) {
		t.Errorf("Files = %v, want %v", res.Files, want)
	}
}

func TestWalk_DoesNotFollowSymbolicLinks(t *testing.T) {
	tr := newTree(t)
	outside := canonicalTempDir(t)
	tr.session(t, outside, "s", time.Now())
	if err := os.Symlink(outside, filepath.Join(tr.root, "link")); err != nil {
		t.Fatal(err)
	}

	res := mustWalk(t, tr)

	if len(res.Files) != 0 || res.visited != 1 {
		t.Errorf("Walk = %+v, want nothing found and one directory visited", res)
	}
}

func TestWalk_EmptyProjectDirectoryIsNotAnError(t *testing.T) {
	tr := newTree(t)
	if err := os.MkdirAll(filepath.Join(tr.project(t, tr.root), "memory"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := mustWalk(t, tr)

	if len(res.Files) != 0 || len(res.Sessions) != 0 || !res.Oldest.IsZero() || res.Gaps.Any() {
		t.Errorf("Walk = %+v, want an empty result", res)
	}
}

func TestWalk_TruncatesAtLimit(t *testing.T) {
	// Directories are visited in lexical order: root, a, a/x, b, c.
	tr := newTree(t)
	tr.mkdir(t, "a/x")
	tr.mkdir(t, "b")
	tr.mkdir(t, "c")
	early := tr.session(t, filepath.Join(tr.root, "a"), "s", time.Now())
	tr.session(t, filepath.Join(tr.root, "c"), "s", time.Now())

	cases := []struct {
		name      string
		limit     int
		visited   int
		truncated bool
		files     []string
	}{
		{"tree smaller than limit", 6, 5, false, nil},
		{"tree exactly at limit", 5, 5, false, nil},
		{"one over keeps what was found", 4, 4, true, []string{early}},
		{"cut before the second session", 2, 2, true, []string{early}},
		{"cut before anything", 0, 0, true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := walk(tr.root, tr.configDir, c.limit)
			if err != nil {
				t.Fatal(err)
			}
			if res.visited != c.visited || res.Gaps.Truncated != c.truncated {
				t.Errorf("visited, Truncated = %d, %v; want %d, %v", res.visited, res.Gaps.Truncated, c.visited, c.truncated)
			}
			if c.files != nil && !slices.Equal(res.Files, c.files) {
				t.Errorf("Files = %v, want %v", res.Files, c.files)
			}
			if c.files == nil && !c.truncated && len(res.Files) != 2 {
				t.Errorf("Files = %v, want both sessions", res.Files)
			}
		})
	}
}

func TestWalk_AmbiguousNameFailsClosed(t *testing.T) {
	tr := newTree(t)
	joined := tr.mkdir(t, "trajector-desktop-probe")
	nested := tr.mkdir(t, "trajector/desktop/probe")
	name := Encode(joined)
	if Encode(nested) != name {
		t.Fatalf("test needs both spellings to share one name: %q vs %q", name, Encode(nested))
	}
	tr.session(t, joined, "s", time.Now())
	control := tr.session(t, filepath.Join(tr.root, "trajector"), "s", time.Now())

	res := mustWalk(t, tr)

	if !slices.Equal(res.Files, []string{control}) {
		t.Errorf("Files = %v, want only the unambiguous directory's session", res.Files)
	}
	want := []Ambiguity{
		{Dir: nested, Name: name, Matches: []string{joined, nested}},
		{Dir: joined, Name: name, Matches: []string{joined, nested}},
	}
	if !reflect.DeepEqual(res.Gaps.Ambiguous, want) {
		t.Errorf("Ambiguous = %+v\nwant %+v", res.Gaps.Ambiguous, want)
	}
}

func TestWalk_NeverListsTheProjectsDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	t.Run("session files are found without listing permission", func(t *testing.T) {
		tr := newTree(t)
		session := tr.session(t, tr.root, "s", time.Now())
		projects := filepath.Join(tr.configDir, "projects")
		if err := os.Chmod(projects, 0o100); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(projects, 0o700) })

		res := mustWalk(t, tr)

		if !slices.Equal(res.Files, []string{session}) {
			t.Errorf("Files = %v, want %v", res.Files, []string{session})
		}
	})
	t.Run("a root that contains the config dir does not descend into projects", func(t *testing.T) {
		root := canonicalTempDir(t)
		tr := tree{root: root, configDir: filepath.Join(root, ".claude")}
		tr.session(t, filepath.Join(root, "elsewhere"), "s", time.Now())
		projects := filepath.Join(tr.configDir, "projects")
		if err := os.Chmod(projects, 0o100); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(projects, 0o700) })

		res := mustWalk(t, tr)

		if len(res.Files) != 0 || len(res.Gaps.Unreadable) != 0 {
			t.Errorf("Walk = %+v, want nothing found and nothing unreadable", res)
		}
	})
}

func TestWalk_CollectsSubagentFiles(t *testing.T) {
	tr := newTree(t)
	main := tr.session(t, tr.root, "sid", time.Now())
	proj := tr.project(t, tr.root)
	agents := filepath.Join(proj, "sid", "subagents")
	agentLog := writeFile(t, filepath.Join(agents, "agent-1.jsonl"))
	agentMeta := writeFile(t, filepath.Join(agents, "agent-1.meta.json"))
	writeFile(t, filepath.Join(agents, "other.jsonl"))
	writeFile(t, filepath.Join(proj, "sid", "tool-results", "toolu_1.txt"))
	writeFile(t, filepath.Join(proj, "memory", "MEMORY.md"))
	writeFile(t, filepath.Join(proj, "orphan", "subagents", "agent-2.jsonl"))

	res := mustWalk(t, tr)

	want := []string{main, agentLog, agentMeta, filepath.Join(proj, "orphan", "subagents", "agent-2.jsonl")}
	slices.Sort(want)
	slices.Sort(res.Files)
	if !slices.Equal(res.Files, want) {
		t.Errorf("Files = %v, want %v", res.Files, want)
	}
	if !slices.Equal(res.Sessions, []string{main}) {
		t.Errorf("Sessions = %v, want %v: agent files are not sessions", res.Sessions, []string{main})
	}
}

func TestWalk_ReportsDirectoriesItCouldNotList(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	tr := newTree(t)
	closed := tr.mkdir(t, "closed")
	tr.session(t, closed, "s", time.Now())
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(closed, 0o755) })

	res := mustWalk(t, tr)

	if !slices.Equal(res.Gaps.Unreadable, []string{closed}) {
		t.Errorf("Unreadable = %v, want %v", res.Gaps.Unreadable, []string{closed})
	}
	if len(res.Sessions) != 1 {
		t.Errorf("Sessions = %v, want the closed directory's own session", res.Sessions)
	}
}

func TestWalk_RejectsBadArguments(t *testing.T) {
	tr := newTree(t)
	cases := []struct {
		name, root, configDir string
	}{
		{"relative root", "proj", tr.configDir},
		{"relative config dir", tr.root, ".claude"},
		{"missing root", filepath.Join(tr.root, "missing"), tr.configDir},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Walk(c.root, c.configDir); err == nil {
				t.Error("Walk returned no error")
			}
		})
	}
}

func TestRegister(t *testing.T) {
	tr := newTree(t)
	a := tr.session(t, tr.root, "a", time.Now())
	b := tr.session(t, tr.mkdir(t, "sub"), "b", time.Now())
	res := mustWalk(t, tr)
	r := follow.Open(t.TempDir())

	if err := Register(r, "0123abcd", res); err != nil {
		t.Fatalf("Register: %v", err)
	}

	files, err := r.Files("0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
	}
	want := []string{a, b}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("registered %v, want %v", got, want)
	}
	if err := Register(r, "", res); err == nil {
		t.Error("Register with an invalid project id returned no error")
	}
}

func TestRegister_RecordsWhatTheWalkCouldNotCover(t *testing.T) {
	r := follow.Open(t.TempDir())
	res := Result{Gaps: follow.Gaps{
		Truncated:  true,
		Ambiguous:  []Ambiguity{{Dir: "/p/a-b", Name: "-p-a-b", Matches: []string{"/p/a-b", "/p/a_b"}}},
		Unreadable: []string{"/p/locked"},
	}}

	if err := Register(r, "hash", res); err != nil {
		t.Fatal(err)
	}

	gaps, err := r.Gaps("hash")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gaps, res.Gaps) {
		t.Errorf("Gaps = %+v, want %+v", gaps, res.Gaps)
	}
}

func TestUnderSessionFiles(t *testing.T) {
	configDir := filepath.Join(string(filepath.Separator), "home", "u", ".claude")
	projects := filepath.Join(configDir, "projects")
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"a session file Claude Code wrote", filepath.Join(projects, "-home-u-proj", "sid.jsonl"), true},
		{"an agent file beside it", filepath.Join(projects, "-home-u-proj", "sid", "subagents", "agent-a.jsonl"), true},
		{"the directory itself", projects, false},
		{"a file beside that directory", filepath.Join(configDir, "settings.json"), false},
		{"a path outside the configuration directory", filepath.Join(string(filepath.Separator), "tmp", "sid.jsonl"), false},
		{"a path of no fixed place", "sid.jsonl", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnderSessionFiles(configDir, c.path); got != c.want {
				t.Errorf("UnderSessionFiles(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}
