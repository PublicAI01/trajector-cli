package follow

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/sessionline"
)

// SubagentsDir is the directory beside a session's main file, under a
// directory named after the session, that holds its agent files.
const SubagentsDir = "subagents"

const (
	linesExt    = ".jsonl"
	metaExt     = ".meta.json"
	agentPrefix = "agent-"

	// maxLineBytes bounds what one line may occupy in memory. A longer
	// line is consumed like a malformed one: reading moves past it and
	// nothing of it is kept.
	maxLineBytes = 32 << 20

	// maxSegmentBytes bounds the lines one segment holds, counted as
	// read — newline included, before redaction — and over the lines
	// kept only. A read stops in front of the line that would pass it,
	// so a segment never holds more, except that a line longer than
	// the bound — up to maxLineBytes — is a segment of its own: a line
	// is never split.
	maxSegmentBytes = 8 << 20

	typeRelocated = "relocated"
)

// droppedTypes are the line types that never enter a segment. The two
// file-history lines carry paths in object keys, where redaction cannot
// reach them, and point at files outside what is read. The bridge line
// names accounts the consent text does not cover.
var droppedTypes = map[string]bool{
	"file-history-snapshot": true,
	"file-history-delta":    true,
	"bridge-session":        true,
}

// ReadOptions says which directories consent covers, so a session that
// moves between directories is read only while it sits in one of them.
type ReadOptions struct {
	// Root is the project directory consent covers.
	Root string
	// Authorized reports whether consent covers the directory a session
	// moved to. Nil means exactly Root and nothing else.
	Authorized func(path string) bool
}

// ReadResult is what one observation of a registered file produced.
type ReadResult struct {
	// Segments holds at most one segment: the complete lines gained
	// since the cursor that are kept, in file order, up to
	// maxSegmentBytes of them.
	Segments []envelope.Segment
	// Snapshots holds at most one snapshot, and only for an agent
	// metadata file whose content differs from the last snapshot taken.
	Snapshots []envelope.MetaSnapshot
	// File is the cursor advanced past what was consumed. It equals the
	// input when the file vanished.
	File     File
	Reaction Reaction
	// Stopped reports that the session moved to a directory consent
	// does not cover. The cursor sits just past that line, and the
	// caller must retire the entry: the bytes beyond it belong to a
	// place consent does not cover, and a cursor cannot say so.
	Stopped bool
	// More reports that the segment reached its bound before the lines
	// observed ran out. The rest is read from the advanced cursor.
	More bool
}

// Read consumes what a registered file gained since its cursor and
// returns the records to store plus the advanced cursor. It never
// writes the file it reads.
//
// Only lines that end in a newline are consumed; a line still being
// written waits for the next read. One read keeps at most one segment
// of lines, and More says when lines past it wait for the next read. A line that is not one of a session
// file is consumed and dropped. A rewritten file is read again from its
// start, and lines whose message id was consumed before are not sent
// again: the copy sent first stands. Lines are stored byte for byte;
// nothing in them is interpreted beyond the three fields that steer
// reading.
func Read(f File, capture envelope.Capture, opts ReadOptions) (ReadResult, error) {
	st, err := StatFile(f.Path)
	if err != nil {
		return ReadResult{}, err
	}
	res := ReadResult{File: f, Reaction: f.React(st)}
	if res.Reaction == Vanished {
		return res, nil
	}
	if strings.HasSuffix(f.Path, metaExt) {
		return readMeta(res, st, capture)
	}
	return readLines(res, st, capture, opts)
}

func readLines(res ReadResult, st Stat, capture envelope.Capture, opts ReadOptions) (ReadResult, error) {
	f := res.File
	start := f.Offset
	// Bytes past the cursor were never consumed, so only a pass that
	// starts over may meet a line it already sent: until that pass
	// reaches the end of the file, the ids consumed before it began are
	// not consumed again, in whichever read the pass meets them.
	skipIDs := f.BeforeRewrite
	if res.Reaction == Rewrite {
		start = 0
		skipIDs = f.MessageIDs
	}
	src, err := os.Open(f.Path)
	if os.IsNotExist(err) {
		res.Reaction = Vanished
		return res, nil
	}
	if err != nil {
		return ReadResult{}, err
	}
	defer src.Close()
	if _, err := src.Seek(start, io.SeekStart); err != nil {
		return ReadResult{}, err
	}

	authorized := opts.Authorized
	if authorized == nil {
		authorized = func(path string) bool { return path == opts.Root }
	}
	consumedBefore := make(map[string]bool, len(f.MessageIDs))
	for _, id := range f.MessageIDs {
		consumedBefore[id] = true
	}
	skipped := make(map[string]bool, len(skipIDs))
	for _, id := range skipIDs {
		skipped[id] = true
	}
	// A pass that starts at byte 0 cannot tell where the session was
	// before a relocated line: whatever precedes a move into consent is
	// let go. A pass that continues from a cursor was already inside.
	inside := start > 0

	var kept bytes.Buffer
	var added []string
	addedSet := map[string]bool{}

	// Only bytes that existed at observation time are read: a line
	// appended after the stat waits for the next read.
	lines := &lineScanner{r: bufio.NewReaderSize(io.LimitReader(src, st.Size-start), 64<<10)}
	offset := start
	for {
		line, n, err := lines.next()
		if err != nil {
			return ReadResult{}, err
		}
		if n == 0 {
			break
		}
		lineStart := offset
		offset += int64(n)
		if line == nil {
			continue
		}
		parsed, ok := sessionline.Parse(line)
		if !ok {
			continue
		}
		fields := parsed.Fields()
		if droppedTypes[fields.Type] {
			continue
		}
		if fields.Type == typeRelocated {
			if !authorized(fields.RelocatedCwd) {
				res.Stopped = true
				break
			}
			if !inside {
				kept.Reset()
				added, addedSet = nil, map[string]bool{}
				inside = true
				continue
			}
		}
		if fields.MessageID != "" && skipped[fields.MessageID] {
			continue
		}
		if kept.Len() > 0 && kept.Len()+len(line) > maxSegmentBytes {
			if !inside {
				// Nothing kept so far may be sent before it is known
				// whether a move into consent follows it.
				end, into, err := relocationAhead(lines, offset, authorized)
				if err != nil {
					return ReadResult{}, err
				}
				if into {
					kept.Reset()
					added, addedSet = nil, map[string]bool{}
					inside = true
					offset = end
					continue
				}
			}
			// The line that would pass the bound is the first of the
			// next segment, and the cursor stops in front of it.
			offset = lineStart
			res.More = true
			break
		}
		if fields.MessageID != "" && !consumedBefore[fields.MessageID] && !addedSet[fields.MessageID] {
			added = append(added, fields.MessageID)
			addedSet[fields.MessageID] = true
		}
		kept.Write(line)
	}

	res.File.Inode, res.File.Size, res.File.Offset = st.Inode, st.Size, offset
	// The caller's slices are never grown in place.
	ids := f.MessageIDs[:len(f.MessageIDs):len(f.MessageIDs)]
	res.File.MessageIDs = append(ids, added...)
	res.File.BeforeRewrite = nil
	if res.More && len(skipIDs) > 0 {
		res.File.BeforeRewrite = slices.Clip(skipIDs)
	}
	if kept.Len() > 0 {
		sessionID, file := identify(f.Path)
		res.Segments = []envelope.Segment{envelope.NewSegment(sessionID, file, f.NextSegment, capture, kept.String())}
		res.File.NextSegment++
	}
	return res, nil
}

// relocationAhead reads on from offset to the first relocated line
// within what was observed. into reports that the line moves the
// session into a directory consent covers, and end is the offset just
// past it. A pass that meets no relocated line, or one that moves the
// session out, reports into as false: what precedes it stays.
func relocationAhead(lines *lineScanner, offset int64, authorized func(string) bool) (end int64, into bool, err error) {
	for {
		line, n, err := lines.next()
		if err != nil || n == 0 {
			return 0, false, err
		}
		offset += int64(n)
		if line == nil || !bytes.Contains(line, []byte(typeRelocated)) {
			continue
		}
		parsed, ok := sessionline.Parse(line)
		if !ok {
			continue
		}
		if fields := parsed.Fields(); fields.Type == typeRelocated {
			return offset, authorized(fields.RelocatedCwd), nil
		}
	}
}

// readMeta takes a snapshot of a whole agent metadata file. The file is
// overwritten in place, so the cursor's message id set holds the record
// id of the last snapshot taken: a new snapshot is due exactly when the
// content names a different one, however the size or inode moved.
func readMeta(res ReadResult, st Stat, capture envelope.Capture) (ReadResult, error) {
	f := res.File
	content, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		res.Reaction = Vanished
		return res, nil
	}
	if err != nil {
		return ReadResult{}, err
	}
	res.File.Inode, res.File.Size, res.File.Offset = st.Inode, int64(len(content)), int64(len(content))
	if len(bytes.TrimSpace(content)) == 0 {
		return res, nil
	}
	sessionID, file := identify(f.Path)
	snap, err := envelope.NewMetaSnapshot(sessionID, file, capture, content)
	if err != nil {
		return ReadResult{}, fmt.Errorf("follow: %s: %w", f.Path, err)
	}
	if len(f.MessageIDs) == 1 && f.MessageIDs[0] == snap.RecordID {
		return res, nil
	}
	res.Snapshots = []envelope.MetaSnapshot{snap}
	res.File.MessageIDs = []string{snap.RecordID}
	return res, nil
}

// identify names the session a file belongs to and the file's place in
// the session's directory: "" for the main file, and the path under the
// session directory for an agent file.
func identify(path string) (sessionID, file string) {
	dir := filepath.Dir(path)
	if filepath.Base(dir) == SubagentsDir {
		return filepath.Base(filepath.Dir(dir)), SubagentsDir + "/" + filepath.Base(path)
	}
	return strings.TrimSuffix(filepath.Base(path), linesExt), ""
}

// Siblings lists the agent files of a session by their fixed place,
// <dir>/<session>/subagents/, next to the session's main file. It is
// the one directory listing this package does, and a session with no
// such directory has no siblings. Only regular files are listed: a
// link could lead outside the session directory.
func Siblings(mainPath string) []string {
	sessionID := strings.TrimSuffix(filepath.Base(mainPath), linesExt)
	dir := filepath.Join(filepath.Dir(mainPath), sessionID, SubagentsDir)
	found := []string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return found
	}
	for _, e := range entries {
		if e.Type().IsRegular() && IsAgentFile(e.Name()) {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	return found
}

// IsSessionFile reports whether name is the name of a session's main
// file, the lines Claude Code appends as the session runs.
func IsSessionFile(name string) bool {
	return strings.HasSuffix(name, linesExt)
}

// IsAgentFile reports whether name is the name of an agent file under
// SubagentsDir: its lines, or the metadata beside them.
func IsAgentFile(name string) bool {
	return strings.HasPrefix(name, agentPrefix) && (strings.HasSuffix(name, linesExt) || strings.HasSuffix(name, metaExt))
}

// lineScanner yields complete lines, newline included, from a reader
// whose read boundaries need not fall on line boundaries.
type lineScanner struct{ r *bufio.Reader }

// next returns the next complete line and the bytes it consumed. A
// line longer than maxLineBytes is consumed but returned as nil. It
// returns n == 0 once no complete line is left: the remaining bytes,
// if any, end without a newline and are not consumed.
func (s *lineScanner) next() (line []byte, n int, err error) {
	for {
		chunk, readErr := s.r.ReadSlice('\n')
		n += len(chunk)
		if n <= maxLineBytes {
			line = append(line, chunk...)
		} else {
			line = nil
		}
		switch readErr {
		case nil:
			return line, n, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			return nil, 0, nil
		default:
			return nil, 0, readErr
		}
	}
}
