package claudesettings

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// ignoreFileName is the project file EnsureGitIgnored appends to and
// RemoveGitIgnored takes lines back out of. One spelling, so the two
// halves of the pair can never disagree about which file they mean.
const ignoreFileName = ".gitignore"

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

	path := filepath.Join(projectRoot, ignoreFileName)
	// The append must never write through a symbolic link: a repository
	// can ship .gitignore as a link to a file outside its own tree, and
	// following it would turn enable into an out-of-tree write at a
	// path the repository chose.
	//
	// Keep the user's chosen permissions on an existing file, as edit
	// does for a settings file; a new one gets the mode git itself would
	// create.
	mode := fs.FileMode(0o644)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return IgnoreSymlinked, nil
		}
		mode = info.Mode().Perm()
	}
	// This is a read-modify-write of a file that belongs to the user and
	// that concurrent trajector processes also append to — enable adds
	// three rules, a diagnostic bundle two. A plain truncating write lost
	// whichever append had read first, and the rule it can lose is the
	// one keeping the injected settings file, which carries a consent
	// token, out of the repository. The same write also left the user's
	// own .gitignore truncated when it died between the truncate and the
	// data. Update is what every other shared file here already uses: one
	// cross-process lock around the whole cycle, and the new content
	// arrives by rename, so a reader sees the old file or the new one.
	// 2026-08-15.
	if err := fsatomic.Update(path, mode, func(existing []byte) ([]byte, error) {
		var b bytes.Buffer
		b.Write(existing)
		if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
			b.WriteByte('\n')
		}
		b.WriteString(rel + "\n")
		return b.Bytes(), nil
	}); err != nil {
		return "", err
	}
	return IgnoreAppended, nil
}

// RemoveGitIgnored takes back out the lines EnsureGitIgnored appended
// for rules — and nothing else in the file. It is the undo half of the
// pair: an enable that fails after appending has to withdraw its own
// ignore lines, and only its own.
//
// Withdrawing them by restoring a whole-file snapshot, which is what
// enable did until 2026-08-27, was wrong twice over. This file belongs
// to the user and concurrent trajector processes append to it — enable
// adds three rules, a diagnostic bundle two — so rewriting it wholesale
// takes whatever landed in between with it. And a wholesale write ran
// even when the enable had appended nothing at all (the rules were
// already covered, or the project is not a git work tree), which on a
// .gitignore that is a symbolic link replaced the link with a regular
// file: exactly the write EnsureGitIgnored refuses to make, arrived at
// through the back door. So the removal reads and rewrites under the
// same cross-process lock the append uses, and answers the symlink
// question the same way the append does.
//
// Only the last occurrence of each rule goes, because that is the one
// the append wrote; a line the user had already put there themselves
// stays. A file that did not end in a newline gains one, which is the
// same normalization the append itself performs.
func RemoveGitIgnored(projectRoot string, rules []string) error {
	if len(rules) == 0 {
		return nil
	}
	path := filepath.Join(projectRoot, ignoreFileName)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		// Already gone, or a link this package never writes through:
		// either way there is nothing of ours in it to take out.
		return nil
	}
	return fsatomic.Update(path, info.Mode().Perm(), func(existing []byte) ([]byte, error) {
		lines := strings.Split(string(existing), "\n")
		for _, rule := range rules {
			for i := len(lines) - 1; i >= 0; i-- {
				// Matched exactly, as written: trimming here could take a
				// user's own line that differs only in trailing space.
				if lines[i] == rule {
					lines = append(lines[:i], lines[i+1:]...)
					break
				}
			}
		}
		return []byte(strings.Join(lines, "\n")), nil
	})
}
