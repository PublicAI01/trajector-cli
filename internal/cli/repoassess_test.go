package cli_test

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
)

// assessAt is the moment every readout in these tests counts up to.
var assessAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// assessRepo is a git repository built commit by commit, for the
// readout to be computed from.
type assessRepo struct {
	t   *testing.T
	dir string
}

// newAssessRepo makes an empty repository in a new directory. The
// developer's git configuration is kept away from the helper and from
// the CLI under test alike.
func newAssessRepo(t *testing.T) *assessRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on this machine")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	r := &assessRepo{t: t, dir: filepath.Join(t.TempDir(), "assessed-repo")}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(nil, "init", "--initial-branch=main")
	return r
}

// change is one commit: the files it writes, who made it, when, and
// its message.
type change struct {
	files   []string
	author  string
	day     int
	message string
}

// commit writes each file of c and commits them. The author time is
// day days into 2026.
func (r *assessRepo) commit(c change) {
	r.t.Helper()
	for _, name := range c.files {
		path := filepath.Join(r.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		content := fmt.Sprintf("%s %d\n", name, c.day)
		if old, err := os.ReadFile(path); err == nil {
			content = string(old) + content
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	author := cmp.Or(c.author, "alice@example.invalid")
	message := cmp.Or(c.message, "change")
	r.git(r.identity(author, c.day), "add", "-A")
	r.git(r.identity(author, c.day), "commit", "-q", "-m", message)
}

// identity is the environment that makes author the author and
// committer of a commit made at day.
func (*assessRepo) identity(author string, day int) []string {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).AddDate(0, 0, day).Format(time.RFC3339)
	name, _, _ := strings.Cut(author, "@")
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + author, "GIT_AUTHOR_DATE=" + at,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + author, "GIT_COMMITTER_DATE=" + at,
	}
}

func (r *assessRepo) git(env []string, args ...string) {
	r.t.Helper()
	cmd := exec.CommandContext(r.t.Context(), "git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// readout runs `repo assess --json` on dir and decodes what it printed.
func readout(t *testing.T, e *clitest.Env, args ...string) map[string]any {
	t.Helper()
	got := e.Run(append([]string{"repo", "assess", "--json"}, args...)...)
	if got.Exit != 0 {
		t.Fatalf("exit = %d, stderr %q", got.Exit, got.Stderr)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(got.Stdout), &m); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, got.Stdout)
	}
	return m
}

func assessEnv(t *testing.T) *clitest.Env {
	t.Helper()
	e := clitest.New(t)
	e.At(assessAt)
	return e
}

func assertFields(t *testing.T, got, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if fmt.Sprint(got[k]) != fmt.Sprint(v) {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

func TestRepoAssess_CountsHowCommitsChangeTestsAndImplementation(t *testing.T) {
	tests := []struct {
		name    string
		history func(r *assessRepo)
		want    map[string]any
	}{
		{
			name: "tests and implementation in one commit",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"calc.go", "calc_test.go"}})
			},
			want: map[string]any{"commits": 1, "test_commits": 1, "src_commits": 1, "cochange": 1, "cochange_src1_5": 1, "cochange_src2p": 0, "cochange_rate": 1},
		},
		{
			name: "only tests",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"tests/test_calc.py"}})
			},
			want: map[string]any{"commits": 1, "test_commits": 1, "src_commits": 0, "cochange": 0},
		},
		{
			name: "only implementation",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"calc.py", "util.py"}})
			},
			want: map[string]any{"commits": 1, "test_commits": 0, "src_commits": 1, "cochange": 0},
		},
		{
			name: "implementation in many files with a test",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"a.rs", "b.rs", "c.rs", "d.rs", "e.rs", "f.rs", "tests/it.rs"}})
			},
			want: map[string]any{"cochange": 1, "cochange_src1_5": 0, "cochange_src2p": 1},
		},
		{
			name: "a test-only commit and then its implementation by the same author",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"calc_test.go"}, day: 1})
				r.commit(change{files: []string{"calc.go"}, day: 2})
			},
			want: map[string]any{"commits": 2, "cochange": 0, "adjacent_pairs": 1, "cochange_or_adj": 1},
		},
		{
			name: "a test-only commit and then implementation by another author",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"calc_test.go"}, day: 1})
				r.commit(change{files: []string{"calc.go"}, day: 2, author: "bob@example.invalid"})
			},
			want: map[string]any{"commits": 2, "adjacent_pairs": 0, "authors": 2},
		},
		{
			name: "a merge commit is left out",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"README.md"}, day: 1})
				r.git(nil, "checkout", "-q", "-b", "side")
				r.commit(change{files: []string{"side.go", "side_test.go"}, day: 2})
				r.git(nil, "checkout", "-q", "main")
				r.commit(change{files: []string{"main.go"}, day: 3})
				r.git(r.identity("alice@example.invalid", 4), "merge", "-q", "--no-ff", "-m", "merge side", "side")
			},
			want: map[string]any{"commits": 3, "cochange": 1, "src_commits": 2, "files": 4},
		},
		{
			name: "paths that are not ASCII",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"pkg/模块.py", "tests/test_模块.py", "文档/说明.md"}})
			},
			want: map[string]any{"cochange": 1, "src_files": 1, "test_files": 1, "files": 3, "main_lang": "python"},
		},
		{
			name: "a coding agent in a Co-authored-by trailer",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"a.go"}, day: 1, message: "add a\n\nCo-authored-by: Claude <noreply@anthropic.com>"})
				r.commit(change{files: []string{"b.go"}, day: 2, message: "add b\n\nCo-authored-by: Bob <bob@example.invalid>"})
				r.commit(change{files: []string{"c.go"}, day: 3})
				r.commit(change{files: []string{"d.go"}, day: 4})
			},
			want: map[string]any{"commits": 4, "agent_commit_share": 0.25, "authors": 1},
		},
		{
			name: "a coding agent as the author",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"a.go"}, day: 1, author: "198982749+Copilot@users.noreply.github.com"})
				r.commit(change{files: []string{"b.go"}, day: 2})
			},
			want: map[string]any{"commits": 2, "agent_commit_share": 0.5, "authors": 2},
		},
		{
			name: "commits authored in two calendar years",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"a.go"}, day: -400})
				r.commit(change{files: []string{"b.go"}, day: -2})
				r.commit(change{files: []string{"c.go"}, day: 1})
			},
			want: map[string]any{"commits": 3, "commits_by_year": "[map[commits:1 year:2024] map[commits:1 year:2025] map[commits:1 year:2026]]", "first_commit": "2024-11-27"},
		},
		{
			name: "a commit authored after the readout's moment",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"a.go"}, day: 1})
				r.commit(change{files: []string{"b.go", "b_test.go"}, day: 300})
			},
			want: map[string]any{"commits": 1, "cochange": 0, "files": 1, "first_commit": "2026-01-02"},
		},
		{
			name: "dependency manifest, lockfile, CI and container files",
			history: func(r *assessRepo) {
				r.commit(change{files: []string{"go.mod", "go.sum", ".github/workflows/ci.yml", "deploy/Dockerfile", "main.go"}})
			},
			want: map[string]any{"has_manifest": true, "has_lock": true, "manifest_lock_paired": true, "has_ci": true, "has_docker": true, "ci_commits": 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newAssessRepo(t)
			tt.history(r)
			assertFields(t, readout(t, assessEnv(t), r.dir), tt.want)
		})
	}
}

func TestRepoAssess_ClassifiesEachPathByItsNameAlone(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"calc.go", "src_files"},
		{"src/app/main.py", "src_files"},
		{"lib/Widget.kt", "src_files"},
		{"calc_test.go", "test_files"},
		{"test_calc.py", "test_files"},
		{"web/button.spec.tsx", "test_files"},
		{"app/src/FooTest.java", "test_files"},
		{"spec/models/user_spec.rb", "test_files"},
		{"tests/data.json", "test_files"},
		{"pkg/testdata/input.txt", "test_files"},
		{"fixtures/user.json", "test_files"},
		{".github/workflows/ci.yml", "neither"},
		{"MyApp_Tests/Calc.cs", "test_files"},
		{"README.md", "neither"},
		{"docs/guide.rst", "neither"},
		{"Makefile", "neither"},
		{"examples/server/main.go", "neither"},
		{"pkg/demo/app.py", "neither"},
		{"samples/basic.ts", "neither"},
		{"lib/samples/basic.ts", "src_files"},
		{"vendor/github.com/x/y.go", "neither"},
		{"third_party/zlib/inflate.c", "neither"},
		{"web/node_modules/left-pad/index.js", "neither"},
		{"dist/bundle.js", "neither"},
		{"docs/conf.py", "neither"},
		{"pkg/docs/render.go", "src_files"},
		{"api/v1/service.pb.go", "neither"},
		{"static/app.min.js", "neither"},
		{"examples/x/test/a.py", "neither"},
		{"tests/examples/a.py", "test_files"},
		{"testdata/vendor/a.go", "test_files"},
		{"test/gen/api.pb.go", "test_files"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			r := newAssessRepo(t)
			r.commit(change{files: []string{tt.path}})
			got := readout(t, assessEnv(t), r.dir)
			want := map[string]any{"files": 1, "src_files": 0, "test_files": 0}
			if tt.want != "neither" {
				want[tt.want] = 1
			}
			assertFields(t, got, want)
		})
	}
}

func TestRepoAssess_ReadsTheWholeRepositoryWhereverItIsPointed(t *testing.T) {
	r := newAssessRepo(t)
	r.commit(change{files: []string{"api/calc.go", "api/calc_test.go", "web/app.ts"}, day: 1})
	r.commit(change{files: []string{"web/app.ts", "web/app.test.ts"}, day: 2})
	e := assessEnv(t)
	root := readout(t, e, r.dir)

	bare := filepath.Join(t.TempDir(), "assessed-bare.git")
	r.git(nil, "clone", "-q", "--bare", r.dir, bare)

	tests := []struct {
		name string
		args func(t *testing.T) []string
	}{
		{"a subdirectory", func(*testing.T) []string { return []string{filepath.Join(r.dir, "api")} }},
		{"a bare clone", func(*testing.T) []string { return []string{bare} }},
		{"the working directory", func(t *testing.T) []string {
			t.Helper()
			t.Chdir(filepath.Join(r.dir, "web"))
			return nil
		}},
		{"a path, whatever GIT_ variables point elsewhere", func(t *testing.T) []string {
			t.Helper()
			other := newAssessRepo(t)
			other.commit(change{files: []string{"lib/other.py"}, day: 5})
			gitDir := filepath.Join(other.dir, ".git")
			for name, value := range map[string]string{
				"GIT_DIR":                          gitDir,
				"GIT_WORK_TREE":                    other.dir,
				"GIT_COMMON_DIR":                   gitDir,
				"GIT_INDEX_FILE":                   filepath.Join(gitDir, "index"),
				"GIT_OBJECT_DIRECTORY":             filepath.Join(gitDir, "objects"),
				"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(gitDir, "objects"),
				"GIT_NAMESPACE":                    "elsewhere",
				"GIT_SHALLOW_FILE":                 filepath.Join(gitDir, "shallow"),
			} {
				t.Setenv(name, value)
			}
			return []string{r.dir}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readout(t, e, tt.args(t)...)
			if fmt.Sprint(got) != fmt.Sprint(root) {
				t.Errorf("readout = %v\nwant the repository root's %v", got, root)
			}
		})
	}
}

func TestRepoAssess_StatesThatAShallowCloneIsIncomplete(t *testing.T) {
	r := newAssessRepo(t)
	r.commit(change{files: []string{"a.go"}, day: 1})
	r.commit(change{files: []string{"b.go"}, day: 2})
	shallow := filepath.Join(t.TempDir(), "assessed-shallow")
	r.git(nil, "clone", "-q", "--depth", "1", "file://"+filepath.ToSlash(r.dir), shallow)
	e := assessEnv(t)

	assertFields(t, readout(t, e, r.dir), map[string]any{"shallow": false, "commits": 2})
	assertFields(t, readout(t, e, shallow), map[string]any{"shallow": true, "commits": 1})
	got := e.Run("repo", "assess", shallow)
	if got.Exit != 0 || !strings.Contains(got.Stdout, "This clone is shallow") {
		t.Errorf("exit %d, stdout %q; want the readout to say it is incomplete", got.Exit, got.Stdout)
	}
}

func TestRepoAssess_RefusesWhatIsNotARepositoryWithCommits(t *testing.T) {
	tests := []struct {
		name       string
		path       func(t *testing.T) string
		wantStderr string
	}{
		{
			name: "a directory outside any repository",
			path: func(t *testing.T) string {
				t.Helper()
				return t.TempDir()
			},
			wantStderr: "not a git repository",
		},
		{
			name: "a repository with no commit",
			path: func(t *testing.T) string {
				t.Helper()
				return newAssessRepo(t).dir
			},
			wantStderr: "no commit on HEAD yet",
		},
		{
			name: "a file",
			path: func(t *testing.T) string {
				t.Helper()
				path := filepath.Join(t.TempDir(), "file.txt")
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantStderr: "not a directory",
		},
		{
			name: "a path that does not exist",
			path: func(t *testing.T) string {
				t.Helper()
				return filepath.Join(t.TempDir(), "missing")
			},
			wantStderr: "not a directory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
			path := tt.path(t)
			got := assessEnv(t).Run("repo", "assess", path)
			if got.Exit != 1 || !strings.Contains(got.Stderr, tt.wantStderr) {
				t.Errorf("exit %d, stderr %q; want 1 and %q", got.Exit, got.Stderr, tt.wantStderr)
			}
			if strings.Contains(got.Stderr, path) || got.Stdout != "" {
				t.Errorf("stdout %q, stderr %q; want nothing that names the path", got.Stdout, got.Stderr)
			}
		})
	}
}

func TestRepoAssess_PrintsCountsAndNothingThatIdentifiesAuthorsOrPaths(t *testing.T) {
	r := newAssessRepo(t)
	r.commit(change{files: []string{"secret-name/calc.go", "secret-name/calc_test.go"}, author: "alice@example.invalid"})
	r.commit(change{files: []string{"b.go"}, day: 1, author: "bob@example.invalid", message: "b\n\nCo-authored-by: Carol <carol@example.invalid>"})
	e := assessEnv(t)

	text := e.Run("repo", "assess", r.dir)
	asJSON := e.Run("repo", "assess", r.dir, "--json")
	for _, out := range []clitest.Result{text, asJSON} {
		if out.Exit != 0 {
			t.Fatalf("exit %d, stderr %q", out.Exit, out.Stderr)
		}
		for _, private := range []string{"alice", "bob", "carol", "Carol", "example.invalid", "secret-name", "calc", r.dir, "assessed-repo"} {
			if strings.Contains(out.Stdout, private) {
				t.Errorf("output contains %q:\n%s", private, out.Stdout)
			}
		}
	}

	var fields map[string]any
	if err := json.Unmarshal([]byte(asJSON.Stdout), &fields); err != nil {
		t.Fatal(err)
	}
	for name := range fields {
		if name == "rule_version" || name == "as_of" || name == "shallow" {
			continue
		}
		if !strings.Contains(text.Stdout, "  "+name+" ") {
			t.Errorf("the printed readout has no line for %s", name)
		}
	}
	if !strings.Contains(text.Stdout, "rule version 1, as of 2026-06-01T00:00:00Z") {
		t.Errorf("stdout = %q, want the rule version and the moment counted up to", text.Stdout)
	}
}

func TestRepoAssess_RefusesArgumentsItDoesNotTake(t *testing.T) {
	tests := []struct {
		name string
		args []string
		exit int
	}{
		{"no subcommand", []string{"repo"}, 2},
		{"an unknown subcommand", []string{"repo", "rate"}, 2},
		{"an unknown flag", []string{"repo", "assess", "--submit"}, 2},
		{"two paths", []string{"repo", "assess", "one", "two"}, 2},
		{"help", []string{"repo", "assess", "--help"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := assessEnv(t).Run(tt.args...)
			if got.Exit != tt.exit || !strings.Contains(got.Stdout+got.Stderr, "usage: trajector repo assess") {
				t.Errorf("exit %d, stdout %q, stderr %q; want %d and the usage line", got.Exit, got.Stdout, got.Stderr, tt.exit)
			}
		})
	}
}
