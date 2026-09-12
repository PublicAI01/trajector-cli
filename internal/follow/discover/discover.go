// Package discover finds the session files Claude Code already keeps for
// an enabled project, so they can be registered for reading.
//
// Claude Code stores the files of a session under
// <configDir>/projects/<name>/, where <name> is computed from the working
// directory the session ran in. This package goes the same direction:
// it walks the project's own directory tree, computes the name each
// directory would have, and looks that one name up. The projects
// directory itself is never listed. The names of projects that were
// never enabled are therefore never constructed, and no code path in
// this package can touch their files.
//
// Only directory names are observed. Nothing here opens a session file:
// the result carries paths and file modification times, never content.
//
// Known gaps, stated as facts: a directory reached through a spelling
// variant on a case-insensitive file system, or a session whose
// directory name was overridden through CLAUDE_CODE_PROJECT_DIR_NAME,
// has a name this walk does not compute and is not found. Only rooted
// POSIX paths are supported; Windows drive letters are not.
package discover

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"

	"github.com/PublicAI01/trajector-cli/internal/follow"
)

// Limit bounds how many directories one walk visits. A walk that would
// visit more stops and reports it: silently leaving a subtree unseen
// would hide sessions the user asked to contribute.
const Limit = 50_000

// nameLimit is the longest name Claude Code writes unhashed.
const nameLimit = 200

// ProjectsDir is the directory under Claude Code's configuration
// directory that holds one directory per working directory Claude Code
// has run in.
const ProjectsDir = "projects"

// Ambiguity is a directory whose encoded name is shared with at least
// one other real directory. Its sessions cannot be attributed to one
// working directory, so none of them are collected. It is the
// registry's own type: a walk's outcome is recorded there.
type Ambiguity = follow.Ambiguity

// Result is what one walk found.
type Result struct {
	// Files are the absolute paths of every session file found: the
	// main <sid>.jsonl files and the agent-*.jsonl and agent-*.meta.json
	// files under <sid>/subagents/.
	Files []string
	// Sessions are the absolute paths of the main <sid>.jsonl files,
	// the subset of Files that is a session's own file. The walk knows
	// which file is which as it reads the directory, so a caller that
	// holds what was found against a registry asks here rather than
	// deciding it again from a path.
	Sessions []string
	// Oldest is the modification time of the oldest main session file,
	// or the zero time when none was found.
	Oldest time.Time
	// visited counts the directories the walk looked at.
	visited int
	// Truncated reports that the tree holds more than Limit directories
	// and the walk stopped at Limit. Everything found before that point
	// is kept.
	Truncated bool
	// Ambiguous lists the directories that were skipped because their
	// encoded name is shared.
	Ambiguous []Ambiguity
	// Unreadable lists the directories whose entries could not be
	// listed. Sessions of directories below them were not looked for.
	Unreadable []string
}

// Encode maps a project directory to the name Claude Code stores its
// session files under.
func Encode(path string) string {
	name, _ := encode(path)
	return name
}

// encode also reports whether the name carries the hash suffix that
// Claude Code appends to over-long names.
func encode(path string) (string, bool) {
	units := utf16.Encode([]rune(norm.NFC.String(path)))
	name := make([]byte, len(units))
	for i, u := range units {
		if isAlnum(u) {
			name[i] = byte(u)
		} else {
			name[i] = '-'
		}
	}
	if len(name) <= nameLimit {
		return string(name), false
	}
	return string(name[:nameLimit]) + "-" + strconv.FormatInt(abs(javaHash(units)), 36), true
}

func isAlnum(u uint16) bool {
	return (u >= 'a' && u <= 'z') || (u >= 'A' && u <= 'Z') || (u >= '0' && u <= '9')
}

// javaHash is the String.hashCode of a UTF-16 string: h = 31*h + unit,
// wrapping in a signed 32-bit integer.
func javaHash(units []uint16) int32 {
	var h int32
	for _, u := range units {
		h = (h << 5) - h + int32(u)
	}
	return h
}

// abs widens before negating so the most negative 32-bit value does not
// stay negative.
func abs(h int32) int64 {
	if h < 0 {
		return -int64(h)
	}
	return int64(h)
}

// Walk finds the session files Claude Code keeps for root and every
// directory under it, by building each directory's encoded name and
// looking it up directly. It never lists the projects directory: a
// directory that is not under root has no name computed for it, so its
// session files stay out of reach.
//
// Every subdirectory is visited, .git and node_modules included;
// symbolic links are not followed. Both root and configDir must be
// absolute. A project directory that holds no session file is not an
// error.
func Walk(root, configDir string) (Result, error) {
	return walk(root, configDir, Limit)
}

func walk(root, configDir string, limit int) (Result, error) {
	if !filepath.IsAbs(root) {
		return Result{}, fmt.Errorf("discover: root %q is not absolute", root)
	}
	if !filepath.IsAbs(configDir) {
		return Result{}, fmt.Errorf("discover: config dir %q is not absolute", configDir)
	}
	root = filepath.Clean(root)
	projects := filepath.Join(configDir, ProjectsDir)

	var res Result
	err := filepath.WalkDir(root, func(dir string, d fs.DirEntry, err error) error {
		if err != nil {
			if dir == root {
				return err
			}
			res.Unreadable = append(res.Unreadable, dir)
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if dir == projects {
			return filepath.SkipDir
		}
		if res.visited == limit {
			res.Truncated = true
			return filepath.SkipAll
		}
		res.visited++

		name, hashed := encode(dir)
		hit := filepath.Join(projects, name)
		if info, err := os.Stat(hit); err != nil || !info.IsDir() {
			return nil
		}
		if !hashed {
			matches := trie(name)
			if len(matches) != 1 || matches[0] != dir {
				res.Ambiguous = append(res.Ambiguous, Ambiguity{Dir: dir, Name: name, Matches: matches})
				return nil
			}
		}
		return collect(hit, &res)
	})
	return res, err
}

// collect gathers the session files of one project directory that has
// been attributed to exactly one working directory.
func collect(hit string, res *Result) error {
	entries, err := os.ReadDir(hit)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(hit, e.Name())
		switch {
		case e.Type().IsRegular() && follow.IsSessionFile(e.Name()):
			info, err := e.Info()
			if err != nil {
				return err
			}
			res.Files = append(res.Files, p)
			res.Sessions = append(res.Sessions, p)
			if res.Oldest.IsZero() || info.ModTime().Before(res.Oldest) {
				res.Oldest = info.ModTime()
			}
		case e.IsDir():
			if err := collectAgents(filepath.Join(p, follow.SubagentsDir), res); err != nil {
				return err
			}
		}
	}
	return nil
}

func collectAgents(dir string, res *Result) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && follow.IsAgentFile(e.Name()) {
			res.Files = append(res.Files, filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// Register puts every file Walk found into the project's registry,
// and records with it what the walk could not cover, so the registry
// can answer for the walk afterwards.
func Register(r *follow.Registry, projectIDHash string, res Result) error {
	for _, f := range res.Files {
		if err := r.Register(projectIDHash, f); err != nil {
			return err
		}
	}
	return r.SetGaps(projectIDHash, res.Gaps())
}

// Gaps is what the walk could not cover, in the registry's own terms.
func (r Result) Gaps() follow.Gaps {
	return follow.Gaps{Truncated: r.Truncated, Ambiguous: r.Ambiguous, Unreadable: r.Unreadable}
}
