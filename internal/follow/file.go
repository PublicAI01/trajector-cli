package follow

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File is one registered file and its cursor.
type File struct {
	Path  string `json:"path"`
	Inode uint64 `json:"inode"`
	Size  int64  `json:"size"`
	// Offset is the byte position reading stopped at.
	Offset int64 `json:"offset"`
	// NextSegment only grows. It survives a Rewrite of the file, so it
	// is stored rather than derived: nothing on disk can give it back.
	NextSegment int `json:"next_segment"`
	// Subpath is where the session ran relative to the project root,
	// with forward slashes. It is absent, never empty, for a session
	// that ran at the root and for a file the backfill walk found,
	// which carries no such position.
	Subpath string `json:"subpath,omitempty"`
	// MessageIDs are the message ids already consumed from this file.
	// It is a set; the array order carries no meaning. An agent
	// metadata file has no messages: there it holds the record id of
	// the last snapshot taken.
	MessageIDs []string `json:"message_ids"`
	// ReadAt is when the file was last read, in RFC 3339, and absent
	// for a file never read. The reader writes it with the cursor; the
	// registry only keeps it.
	ReadAt string `json:"read_at,omitempty"`
	// Retired is why this file is no longer read, and is absent for a
	// file still read. An entry gains it instead of leaving the
	// registry, so that a later registration of the same path knows
	// the file is not to be read from its start again.
	//
	// The field was added to a layout that already stood, and it is
	// the only compatibility rule this layout needs: every other field
	// keeps its name and its meaning, a registry written without the
	// field holds no retired entry, and a build that does not know the
	// field reads a retired entry as one still to read.
	Retired Retirement `json:"retired,omitempty"`
	// LastEvent is when a session hook last named this file, in RFC
	// 3339, and absent for a file no hook named since it was
	// registered. PID is the process the session runs in, as the hook
	// that named the file reported it, and 0 when unknown. Together
	// they say whether the file is hot: one a session is writing right
	// now, worth observing between hooks. Both are local state like
	// the cursor and never leave the registry.
	LastEvent string `json:"last_event,omitempty"`
	PID       int    `json:"pid,omitzero"`
	// Told records that this session was already told, on a hook of
	// its own, that the device stopped recording. It is written once
	// per session and never read back into a cursor: a session that
	// hears the same line on every turn stops reading it, so the
	// notice is worth exactly one turn of the user's attention.
	Told bool `json:"told,omitzero"`
}

// hotWindow is how long a hook event keeps a file hot on its own,
// with no process to vouch for the session: a session that says
// nothing for a day is over, whatever its process is doing.
const hotWindow = 24 * time.Hour

// Hot reports whether f is being written by a session right now, as
// far as the registry can tell: the process the last hook reported is
// alive, or a hook named the file within the last day. A cold file is
// never observed between hooks; a hook naming it again makes it hot.
// alive answers whether a process id names a running process.
func (f File) Hot(now time.Time, alive func(pid int) bool) bool {
	if f.Retired != "" {
		return false
	}
	if f.PID > 0 && alive(f.PID) {
		return true
	}
	return f.QuietFor(now) < hotWindow
}

// QuietFor is how long f has gone since a hook last named it. A file
// no hook named since it was registered, and one whose time was not
// written in the layout's own form, has been quiet for as long as
// this type can state.
func (f File) QuietFor(now time.Time) time.Duration {
	at, err := time.Parse(readAtLayout, f.LastEvent)
	if err != nil {
		return math.MaxInt64
	}
	return now.Sub(at)
}

// Attended reports whether the process the last hook reported for f
// is still running. A file with no process is unattended.
func (f File) Attended(alive func(pid int) bool) bool {
	return f.Retired == "" && f.PID > 0 && alive(f.PID)
}

// Group lists the entries of files that belong to the same session as
// path, in the order given. Heat is a property of the session, not of
// one of its files: a hook names the main file, but the turn it
// reports may have been a subagent's, written to an agent file no
// hook ever names. Marking one file of a session marks them all, and
// what is read on a hook's word is the whole group.
func Group(files []File, path string) []File {
	session := sessionOf(path)
	group := []File{}
	for _, f := range files {
		if sessionOf(f.Path) == session {
			group = append(group, f)
		}
	}
	return group
}

// sessionOf names the session a path belongs to: the session's own
// directory, which is the main file's path without the lines
// extension. It is the one definition of what makes a session's
// files one group, and it is path arithmetic alone — a group holds
// together whether or not the files are on disk. A main file
// <dir>/<sid>.jsonl and the agent files under <dir>/<sid>/subagents/
// yield the same name. A path that is neither belongs to no session
// but its own.
func sessionOf(path string) string {
	dir := filepath.Dir(path)
	if filepath.Base(dir) == SubagentsDir && IsAgentFile(filepath.Base(path)) {
		return filepath.Dir(dir)
	}
	if name, ok := strings.CutSuffix(path, linesExt); ok {
		return name
	}
	return path
}

// Split parts files into the hot ones and the cold ones, each in the
// order given.
func Split(files []File, now time.Time, alive func(pid int) bool) (hot, cold []File) {
	for _, f := range files {
		if f.Hot(now, alive) {
			hot = append(hot, f)
		} else {
			cold = append(cold, f)
		}
	}
	return hot, cold
}

// Retirement is why an entry's cursor can never advance again. Its
// values are written to the registry, so they are part of the layout.
type Retirement string

// Relocated: the session moved to a directory consent does not cover.
// It is the one reason an entry is kept after its reading stopped:
// the file stays on disk, so a later search of the project finds it
// again and would otherwise read it from its start a second time.
const Relocated Retirement = "relocated"

// MainSession reports whether f is a session's own file rather than
// an agent file kept beside it under subagents/. Counting sessions
// means counting these.
func (f File) MainSession() bool {
	_, file := identify(f.Path)
	return file == "" && strings.HasSuffix(f.Path, linesExt)
}

// LastRead is when the file was last read. A file never read, and one
// whose time was not written in the layout's own form, has none. It is
// the one reading of ReadAt, as the reader is the one writer of it.
func (f File) LastRead() (time.Time, bool) {
	at, err := time.Parse(readAtLayout, f.ReadAt)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// readAtLayout is how a cursor states when it last moved.
const readAtLayout = time.RFC3339

// Stat is what the reader observes about a registered file before
// deciding how to continue.
type Stat struct {
	Exists bool
	Inode  uint64
	Size   int64
}

// StatFile observes path. A path that does not exist is an observation,
// not a failure. Inode is 0 where the platform has no stable file
// identity to report; a cursor compares inodes only when both sides
// know theirs.
func StatFile(path string) (Stat, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Stat{}, nil
	}
	if err != nil {
		return Stat{}, err
	}
	return Stat{Exists: true, Inode: inodeOf(info), Size: info.Size()}, nil
}

// Reaction is how reading must continue after observing a file.
type Reaction int

const (
	// Continue: read on from Offset.
	Continue Reaction = iota
	// Rewrite: the file is not the one the cursor describes. The
	// reader starts over from byte 0; ids already in MessageIDs are
	// not consumed again; NextSegment continues from its stored value
	// and is never reset.
	Rewrite
	// Vanished: the file is gone, and the cursor is void.
	Vanished
)

// React decides how reading of f continues given st. An inode of 0 on
// either side is unknown and takes no part in the decision.
func (f File) React(st Stat) Reaction {
	switch {
	case !st.Exists:
		return Vanished
	case f.Inode != 0 && st.Inode != 0 && st.Inode != f.Inode:
		return Rewrite
	case st.Size < f.Size:
		return Rewrite
	default:
		return Continue
	}
}

// Behind is how many bytes of st lie past f's cursor when reading
// continues, and 0 otherwise.
func (f File) Behind(st Stat) int64 {
	if f.React(st) != Continue {
		return 0
	}
	return st.Size - f.Offset
}
