package follow

import (
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/sessionline"
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
	// BeforeRewrite holds the message ids consumed before the file was
	// rewritten, while the pass that reads the rewritten file from its
	// start has not reached its end, and is absent otherwise. That pass
	// may take several reads, and each of them skips these ids as the
	// first one did. A build that does not know the field continues
	// such a pass as an ordinary read, and sends again the lines of
	// those ids that it meets.
	BeforeRewrite []string `json:"before_rewrite,omitempty"`
	// ReadAt is when a read of the file last found something new, in
	// RFC 3339, and absent for a file never read. The reader writes it
	// with the cursor, and a read that finds nothing new writes
	// neither; the registry only keeps it.
	ReadAt string `json:"read_at,omitempty"`
	// Outside records that the session moved to a directory consent
	// does not cover and has not come back. The reader moves the
	// cursor past what the session writes there and keeps none of it,
	// and keeps its lines again once a relocated line brings it back;
	// a rewrite of the file does not bring it back. It is set from the
	// file's own lines only. An agent file holds no relocated line, and
	// is read as outside while its session's main file is.
	Outside bool `json:"outside,omitzero"`
	// LastEvent is when a session hook last named this file's session,
	// in RFC 3339, and absent for a session no hook named since it was
	// registered. PID is the process the session runs in, as the hook
	// reported it, and 0 when unknown. Together they say whether the
	// file is hot: one a session is writing right now, worth observing
	// between hooks. They are the heat of the session, read with the
	// entry and kept apart from it, and never leave this device.
	LastEvent string `json:"-"`
	PID       int    `json:"-"`
	// EmptyReasoningBlocks and ReasoningBlocksWithText count the
	// reasoning blocks of the assistant lines read from this file and
	// kept: those whose reasoning field is empty, and those whose field
	// holds text. They hold counts only, never what a field holds.
	// LaunchSurface is how Claude Code was started, as the last kept
	// line that states it says, and absent until a line states it. The
	// reader writes all three with the cursor; they never leave the
	// registry.
	EmptyReasoningBlocks    int    `json:"empty_reasoning_blocks,omitzero"`
	ReasoningBlocksWithText int    `json:"reasoning_blocks_with_text,omitzero"`
	LaunchSurface           string `json:"launch_surface,omitempty"`
}

// Reasoning is what the reasoning counts of f hold.
func (f File) Reasoning() sessionline.ReasoningBlocks {
	return sessionline.ReasoningBlocks{Empty: f.EmptyReasoningBlocks, WithText: f.ReasoningBlocksWithText}
}

// advancedTo is f with the fields a reader owns taken from read: where
// reading stands, what it consumed, when it last moved, where the
// session is, and what the kept lines said about reasoning and the
// launch surface. Every other field has a writer of its own — the
// registration, the hooks that mark a session hot or cold — and an
// entry a reader writes back keeps those as they stand now, not as
// they stood when the read began.
func (f File) advancedTo(read File) File {
	f.Inode, f.Size, f.Offset, f.NextSegment = read.Inode, read.Size, read.Offset, read.NextSegment
	f.MessageIDs, f.BeforeRewrite = read.MessageIDs, read.BeforeRewrite
	f.ReadAt, f.Outside = read.ReadAt, read.Outside
	f.EmptyReasoningBlocks, f.ReasoningBlocksWithText = read.EmptyReasoningBlocks, read.ReasoningBlocksWithText
	f.LaunchSurface = read.LaunchSurface
	return f
}

// changedBy reports whether advancedTo(read) differs from f in any
// field but ReadAt: whether a read that ended at read found anything
// the entry does not already say. It names the same fields advancedTo
// takes.
func (f File) changedBy(read File) bool {
	return f.Inode != read.Inode || f.Size != read.Size || f.Offset != read.Offset || f.NextSegment != read.NextSegment ||
		!slices.Equal(f.MessageIDs, read.MessageIDs) || !slices.Equal(f.BeforeRewrite, read.BeforeRewrite) ||
		f.Outside != read.Outside ||
		f.Reasoning() != read.Reasoning() || f.LaunchSurface != read.LaunchSurface
}

// hotWindow is how long a hook event keeps a file hot on its own,
// with no process to vouch for the session: a session that says
// nothing for a day is over, whatever its process is doing.
const hotWindow = 24 * time.Hour

// Hot reports whether f is being written by a session right now, as
// far as the registry can tell: the process the last hook reported is
// alive, or a hook named the file within the last day. A cold file is
// never observed between hooks; a hook naming it again makes it hot.
// A file outside is never hot: a hook naming it has it read all the
// same. alive answers whether a process id names a running process.
func (f File) Hot(now time.Time, alive func(pid int) bool) bool {
	if f.Outside {
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
// is still running. A file with no process, and a file outside, is
// unattended.
func (f File) Attended(alive func(pid int) bool) bool {
	return !f.Outside && f.PID > 0 && alive(f.PID)
}

// group lists the entries of files that belong to the same session as
// path, in the order given. Heat is a property of the session, not of
// one of its files: a hook names the main file, but the turn it
// reports may have been a subagent's, written to an agent file no
// hook ever names. Marking one file of a session marks them all, and
// what is read on a hook's word is the whole group.
func group(files []File, path string) []File {
	session := SessionOf(path)
	members := []File{}
	for _, f := range files {
		if SessionOf(f.Path) == session {
			members = append(members, f)
		}
	}
	return members
}

// SessionOf names the session a path belongs to: the session's own
// directory, which is the main file's path without the lines
// extension. It is the one definition of what makes a session's
// files one group, and it is path arithmetic alone — a group holds
// together whether or not the files are on disk. A main file
// <dir>/<sid>.jsonl and the agent files under <dir>/<sid>/, at the
// places agentFileOf accepts, yield the same name. A path that is
// neither belongs to no session but its own.
func SessionOf(path string) string {
	if sessionDir, _, at := agentFileOf(path); at != notAgentFile {
		return sessionDir
	}
	if name, ok := strings.CutSuffix(path, linesExt); ok {
		return name
	}
	return path
}

// KeptFromEarlierBuilds reports whether f is a file that builds which
// do not know where it is kept never read: an agent file of a workflow
// run. What was written to it while such a build was the one reading
// is still unread when this build takes over, whatever its heat says.
func (f File) KeptFromEarlierBuilds() bool {
	_, _, at := agentFileOf(f.Path)
	return at == inWorkflowRun
}

// MainSession reports whether f is a session's own file rather than
// an agent file kept under the session's directory. Counting sessions
// means counting these.
func (f File) MainSession() bool {
	_, file := identify(f.Path)
	return file == "" && strings.HasSuffix(f.Path, linesExt)
}

// NeverRead reports an entry nothing has ever consumed: no segment
// taken from it and no byte of it read. It is the state a
// registration leaves behind, and it separates a file still waiting
// for its first reading from one whose reading is merely up to date.
func (f File) NeverRead() bool { return f.NextSegment == 0 && f.Offset == 0 }

// LastRead is when a read of the file last found something new. A file
// never read, and one whose time was not written in the layout's own
// form, has none. It is
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
	// not consumed again, in this read or in the reads that finish the
	// pass; NextSegment continues from its stored value and is never
	// reset.
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

// LeftToRead reports whether a read of f, given st, has anything to
// do: bytes past the cursor, a file that is not the one the cursor
// describes, or a file that is gone. A read of a file it answers false
// for finds nothing new.
func (f File) LeftToRead(st Stat) bool {
	return f.React(st) != Continue || f.Behind(st) > 0
}
