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

func (o ReadOptions) authorized() func(path string) bool {
	if o.Authorized != nil {
		return o.Authorized
	}
	return func(path string) bool { return path == o.Root }
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
// of lines, and More says when lines past it wait for the next read. A
// line that is not one of a session file is consumed and dropped, and
// so is every line the session wrote while it was in a directory
// consent does not cover: the cursor moves past them and the entry
// says the session is outside until a relocated line brings it back,
// however the file is rewritten in the meantime. A rewritten file is
// read again from its start, and lines whose message id was consumed
// before are not sent again: the copy sent first stands. Lines are
// stored byte for byte; nothing in them is interpreted beyond the
// three fields that steer reading.
//
// Read judges a file by its own lines alone. An agent file holds no
// line that says where its session is; a Reader judges it by its
// session's main file as well.
func Read(f File, capture envelope.Capture, opts ReadOptions) (ReadResult, error) {
	st, err := StatFile(f.Path)
	if err != nil {
		return ReadResult{}, err
	}
	return readObserved(f, st, capture, opts, false)
}

// readObserved is Read of f as st observed it. sessionOutside keeps
// nothing of what the read consumes, while the cursor moves on as it
// would: it is how an agent file is read while its session is outside.
func readObserved(f File, st Stat, capture envelope.Capture, opts ReadOptions, sessionOutside bool) (ReadResult, error) {
	res := ReadResult{File: f, Reaction: f.React(st)}
	if res.Reaction == Vanished {
		return res, nil
	}
	if strings.HasSuffix(f.Path, metaExt) {
		return readMeta(res, st, capture, sessionOutside)
	}
	return readLines(res, st, capture, opts, sessionOutside)
}

func readLines(res ReadResult, st Stat, capture envelope.Capture, opts ReadOptions, sessionOutside bool) (ReadResult, error) {
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

	authorized := opts.authorized()
	consumedBefore := make(map[string]bool, len(f.MessageIDs))
	for _, id := range f.MessageIDs {
		consumedBefore[id] = true
	}
	skipped := make(map[string]bool, len(skipIDs))
	for _, id := range skipIDs {
		skipped[id] = true
	}
	// Where the session is decides what is kept: nothing of what it
	// writes while it is in a directory consent does not cover. A pass
	// that continues from a cursor knows where the session is from the
	// cursor, and so does any pass, from byte 0 too, of a file whose
	// cursor says the session is outside: only a relocated line into
	// consent brings it back, whatever a rewrite left in the file. Any
	// other pass that starts at byte 0 cannot tell until the first
	// relocated line: when it moves the session into consent, what
	// precedes it was written outside and is let go; when it moves the
	// session out, what precedes it was written inside and stays.
	outside := f.Outside
	settled := start > 0 || f.Outside

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
			// A relocated line that crosses the edge of consent is
			// never kept: moving out, it names where the session went.
			into := authorized(fields.RelocatedCwd)
			switch {
			case !into:
				settled, outside = true, true
				continue
			case !settled:
				kept.Reset()
				added, addedSet = nil, map[string]bool{}
				settled = true
				continue
			case outside:
				outside = false
				continue
			}
		}
		if outside || sessionOutside {
			continue
		}
		if fields.MessageID != "" && skipped[fields.MessageID] {
			continue
		}
		if kept.Len() > 0 && kept.Len()+len(line) > maxSegmentBytes {
			if !settled {
				// Nothing kept so far may be sent before it is known
				// whether a move into consent follows it.
				end, into, err := relocationAhead(lines, offset, authorized)
				if err != nil {
					return ReadResult{}, err
				}
				if into {
					kept.Reset()
					added, addedSet = nil, map[string]bool{}
					settled = true
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
	res.File.Outside = outside
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
// session out, reports into as false: what precedes it was written
// inside.
func relocationAhead(lines *lineScanner, offset int64, authorized func(string) bool) (end int64, into bool, err error) {
	for {
		line, n, err := lines.next()
		if err != nil || n == 0 {
			return 0, false, err
		}
		offset += int64(n)
		if cwd, ok := relocatedCwd(line); ok {
			return offset, authorized(cwd), nil
		}
	}
}

// relocatedCwd reports the directory a relocated line moves the
// session to, and false for any other line.
func relocatedCwd(line []byte) (string, bool) {
	if line == nil || !bytes.Contains(line, []byte(typeRelocated)) {
		return "", false
	}
	parsed, ok := sessionline.Parse(line)
	if !ok {
		return "", false
	}
	fields := parsed.Fields()
	return fields.RelocatedCwd, fields.Type == typeRelocated
}

// sessionWasOutside reports whether the session main belongs to is in
// a directory consent does not cover, or was at any point of what its
// main file holds past the cursor: the cursor says the session is
// outside, or a relocated line not yet consumed moves it out, or moves
// it in at a point before which a pass from byte 0 lets everything go.
//
// It answers for the whole of what main holds unread, not for when
// each line of another file was written, so it may call outside a line
// written inside and never the other way: keeping nothing written
// outside comes before keeping everything written inside.
func sessionWasOutside(main File, opts ReadOptions) (bool, error) {
	if main.Outside {
		return true, nil
	}
	st, err := StatFile(main.Path)
	if err != nil {
		return false, err
	}
	start := main.Offset
	switch main.React(st) {
	case Vanished:
		return false, nil
	case Rewrite:
		start = 0
	}
	src, err := os.Open(main.Path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer src.Close()
	if _, err := src.Seek(start, io.SeekStart); err != nil {
		return false, err
	}
	authorized := opts.authorized()
	fromCursor := start > 0
	lines := &lineScanner{r: bufio.NewReaderSize(io.LimitReader(src, st.Size-start), 64<<10)}
	for {
		line, n, err := lines.next()
		if err != nil || n == 0 {
			return false, err
		}
		if cwd, ok := relocatedCwd(line); ok && (!fromCursor || !authorized(cwd)) {
			return true, nil
		}
	}
}

// readMeta takes a snapshot of a whole agent metadata file. The file is
// overwritten in place, so the cursor's message id set holds the record
// id of the last snapshot taken: a new snapshot is due exactly when the
// content names a different one, however the size or inode moved. With
// sessionOutside the content counts as taken and no snapshot is made,
// so what the file held while its session was outside is never sent.
func readMeta(res ReadResult, st Stat, capture envelope.Capture, sessionOutside bool) (ReadResult, error) {
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
	res.File.MessageIDs = []string{snap.RecordID}
	if !sessionOutside {
		res.Snapshots = []envelope.MetaSnapshot{snap}
	}
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
