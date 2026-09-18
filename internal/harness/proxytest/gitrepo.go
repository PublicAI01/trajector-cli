package proxytest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitRepo is a git repository of the test's own, driven with the git
// this machine has. Every command runs against the directory the test
// named, with the developer's own git configuration kept out, so what
// a test observes comes from the temp tree and from nowhere else.
type GitRepo struct {
	t   *testing.T
	dir string
}

// NewGitRepo makes dir a repository with an identity of the test's
// own. It skips the test where this machine has no git: a repository
// is the one thing the harness cannot stand in for.
func NewGitRepo(t *testing.T, dir string) *GitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on this machine, so there is nothing to observe with")
	}
	r := &GitRepo{t: t, dir: dir}
	r.run("init", "--initial-branch=main")
	r.run("config", "user.name", "Test")
	r.run("config", "user.email", "test@example.invalid")
	return r
}

// Commit writes one file under the repository and commits it,
// returning the commit it made.
func (r *GitRepo) Commit(name, content string) string {
	r.t.Helper()
	path := filepath.Join(r.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.run("add", "-A")
	r.run("commit", "-m", "change "+name)
	return r.run("rev-parse", "HEAD")
}

// Ignores reports whether git ignores path inside the repository.
func (r *GitRepo) Ignores(path string) bool {
	r.t.Helper()
	err := r.command("check-ignore", "-q", "--", path).Run()
	if err == nil {
		return true
	}
	// One is how check-ignore states that nothing ignores the path,
	// and is the only failure that is an answer rather than a fault.
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return false
	}
	r.t.Fatalf("git check-ignore: %v", err)
	return false
}

func (r *GitRepo) run(args ...string) string {
	r.t.Helper()
	out, err := r.command(args...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// command runs git in the repository with every configuration and
// ignore file the developer's machine would otherwise contribute
// pointed at the null device, so the repository itself is the only
// thing that decides what git answers.
func (r *GitRepo) command(args ...string) *exec.Cmd {
	global := []string{"-c", "core.excludesFile=" + os.DevNull, "-C", r.dir}
	cmd := exec.Command("git", append(global, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
	)
	return cmd
}
