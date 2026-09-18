// Package gitsnapshot observes a git repository with the git command
// line: which commit is checked out, which branch names it, and which
// paths differ between two commits. It reads no file content and writes
// nothing to the repository.
//
// Every value it returns is what git printed. Nothing here decides what
// a value means — whether a directory is a worktree, whether a commit
// came from an agent, whether a change belongs to the session that ran
// — because a reading made on this machine would have to be made again
// wherever the record lands.
//
// What it does decide is when a repository is worth observing and
// which commit the observation is read against, because it is also
// where the commit this device last saw is kept: the rule and the
// value it compares against stay together.
//
// Observing is on a session's critical path, so every command runs
// under a deadline and a repository that cannot answer inside it is
// simply not observed.
package gitsnapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

// defaultTimeout bounds one observation. It is generous for the
// commands below on any repository and short enough that a session
// never notices a repository that will not answer.
const defaultTimeout = 5 * time.Second

// commitID is the shape of every commit this package will pass back to
// git. A value that does not match it never reaches a command line, so
// a stored or printed value can never be read as an option.
var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidCommitID reports whether id is a full commit identifier. The
// local state that remembers a commit and the observer that compares
// against it share this one definition.
func ValidCommitID(id string) bool { return commitID.MatchString(id) }

// Position is where a repository's HEAD stands: the branch name git
// prints for it, the commit it names, and that commit's first parent.
// Parent is empty at a root commit, which is a fact about the commit
// and not a failure to read one.
type Position struct {
	Branch string
	Head   string
	Parent string
}

// Observer reads one directory's repository.
type Observer struct {
	// Dir is the directory the commands run in. Git resolves it to
	// whichever repository or linked worktree contains it.
	Dir string
	// Timeout bounds each command. Zero selects the default.
	Timeout time.Duration
}

// errNoCommit reports a directory that is not a repository, or a
// repository with no commit yet. Both are the same answer to the
// caller: there is nothing to observe.
var errNoCommit = errors.New("gitsnapshot: no commit to observe")

// Position reads where HEAD stands. It fails when the directory is not
// inside a repository or the repository has no commit.
func (o Observer) Position(ctx context.Context) (Position, error) {
	branch, err := o.run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Position{}, errNoCommit
	}
	// One commit and its parents on one line, so the first parent is
	// read from the same output that names HEAD and the two can never
	// describe different moments.
	line, err := o.run(ctx, "rev-list", "--max-count=1", "--parents", "HEAD")
	if err != nil {
		return Position{}, errNoCommit
	}
	ids := strings.Fields(line)
	if len(ids) == 0 || !ValidCommitID(ids[0]) {
		return Position{}, errNoCommit
	}
	p := Position{Branch: strings.TrimSpace(branch), Head: ids[0]}
	if len(ids) > 1 && ValidCommitID(ids[1]) {
		p.Parent = ids[1]
	}
	return p, nil
}

// Changes lists what git prints for the pair of commits, one entry per
// printed line, with no rename or copy detection asked for. A line git
// prints in a shape this parser does not recognize is left out rather
// than guessed at; every other line is copied field for field.
func (o Observer) Changes(ctx context.Context, base, head string) ([]envelope.Change, error) {
	if !ValidCommitID(base) || !ValidCommitID(head) {
		return nil, fmt.Errorf("gitsnapshot: %q..%q is not a pair of commit identifiers", base, head)
	}
	out, err := o.run(ctx, "diff-tree", "-r", "--no-commit-id", "--raw", "--abbrev=40", base, head)
	if err != nil {
		return nil, err
	}
	var changes []envelope.Change
	for line := range strings.SplitSeq(strings.TrimSuffix(out, "\n"), "\n") {
		if c, ok := parseRawLine(line); ok {
			changes = append(changes, c)
		}
	}
	return changes, nil
}

// parseRawLine reads one line of git's raw diff output:
//
//	:<old mode> <new mode> <old blob> <new blob> <status>\t<path>
//
// The status letter and both blob identifiers are copied as printed —
// git spells an absent side as forty zeros, and that is left standing.
func parseRawLine(line string) (envelope.Change, bool) {
	if !strings.HasPrefix(line, ":") {
		return envelope.Change{}, false
	}
	meta, path, ok := strings.Cut(line[1:], "\t")
	if !ok || path == "" {
		return envelope.Change{}, false
	}
	fields := strings.Fields(meta)
	if len(fields) != 5 {
		return envelope.Change{}, false
	}
	return envelope.Change{Path: path, Status: fields[4], OldBlob: fields[2], NewBlob: fields[3]}, true
}

// run executes one git command in Dir under the observer's deadline and
// returns its standard output. Nothing of the command's standard error
// is kept: a failure here is answered by not observing, never by
// telling the session about it.
func (o Observer) run(ctx context.Context, args ...string) (string, error) {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", o.Dir}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}
