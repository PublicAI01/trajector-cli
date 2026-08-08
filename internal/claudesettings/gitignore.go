package claudesettings

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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

// EnsureGitIgnored makes sure rel — a slash-separated path or ignore
// pattern, relative to the project root — is ignored by git, appending
// it verbatim to the project's .gitignore when it is not. A trailing
// slash is kept: it is what scopes a rule to directories. Callers pass
// the files this tool drops into a user's project; none of them may
// end up committed.
func EnsureGitIgnored(projectRoot, rel string) (IgnoreAction, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return IgnoreSkipped, nil
	}
	// check-ignore runs inside a directory the repository controls, and
	// repository-level configuration is loaded there. fsmonitor is the
	// one such setting that names a command to execute, so it is pinned
	// off; system and global configuration stay untouched — the user's
	// own excludes file is a legitimate ignore source.
	check := exec.Command(gitPath, "-c", "core.fsmonitor=false", "check-ignore", "-q", "--", rel)
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
	b.WriteString(rel + "\n")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		return "", err
	}
	return IgnoreAppended, nil
}
