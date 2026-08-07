package claudesettings

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IgnoreAction reports what EnsureGitIgnored did.
type IgnoreAction string

const (
	// IgnoreCovered: the file is already ignored.
	IgnoreCovered IgnoreAction = "covered"
	// IgnoreAppended: an ignore line was added to the project's
	// .gitignore.
	IgnoreAppended IgnoreAction = "appended"
	// IgnoreSkipped: the project is not a git work tree or git is not
	// available, so there is nothing to leak through.
	IgnoreSkipped IgnoreAction = "skipped"
	// IgnoreSymlinked: the project's .gitignore is a symbolic link, so
	// nothing was written; the caller must tell the user to add the
	// ignore themselves.
	IgnoreSymlinked IgnoreAction = "symlinked"
)

// EnsureGitIgnored makes sure rel (slash-separated, relative to the
// project root) is ignored by git, appending it to the project's
// .gitignore when it is not. The injected settings file embeds a
// consent token and must never end up in the user's repository.
func EnsureGitIgnored(projectRoot, rel string) (IgnoreAction, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return IgnoreSkipped, nil
	}
	check := exec.Command(gitPath, "check-ignore", "-q", "--", rel)
	check.Dir = projectRoot
	if err := check.Run(); err == nil {
		return IgnoreCovered, nil
	} else if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		// check-ignore exits 1 for "tracked-path, not ignored" and
		// anything else (128) for "not a repository": only the former
		// needs fixing.
		return IgnoreSkipped, nil
	}

	path := filepath.Join(projectRoot, ".gitignore")
	// The append must never write through a symbolic link: a repository
	// can ship .gitignore as a link to a file outside its own tree, and
	// following it would turn enable into an out-of-tree write at a
	// path the repository chose.
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return IgnoreSymlinked, nil
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var b bytes.Buffer
	b.Write(existing)
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		b.WriteByte('\n')
	}
	b.WriteString(strings.TrimSuffix(rel, "/") + "\n")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		return "", err
	}
	return IgnoreAppended, nil
}
