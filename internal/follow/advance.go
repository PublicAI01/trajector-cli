package follow

import (
	"errors"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

// Storing is what a store did with the records of one read.
type Storing int

const (
	// Stored: every record of the read is on disk, so the cursor may
	// advance past what produced them.
	Stored Storing = iota
	// NotStored: this read's records are not stored and the cursor
	// must stay where it is. The project's next entry is still read:
	// what stopped this one may not hold for it.
	NotStored
	// Held: this read's records are not stored, the cursor must stay,
	// and no further entry of the project is read in this run. It is
	// the answer for a condition that holds for every entry alike, such
	// as no room left to store in.
	Held
	// Paused: as Held, for content that this build must not store. What
	// the store noticed about that content is kept all the same: it is
	// what says why reading stopped, and no cursor write follows to
	// carry it.
	Paused
)

// Reader advances the cursors of one project's registered entries. It
// holds the whole rule for one entry: read what the file gained since
// its cursor, hand what that produced to Store, and write the advanced
// cursor back only once every record is stored. A caller chooses which
// entries to hand it and what storing means; nothing outside decides
// when a cursor moves.
type Reader struct {
	// Registry holds the entries whose cursors advance.
	Registry *Registry
	// ProjectIDHash names the project the entries belong to.
	ProjectIDHash string
	// Options say which directories consent covers.
	Options ReadOptions
	// Capture is what every record of this run states about the
	// exchange it came from. Each record takes the position of the
	// entry that produced it; the rest is the caller's to fill.
	Capture envelope.Capture
	// Store lands the records of one read and reports what became of
	// them, and what it noticed about the lines they hold. It is the one
	// judge of whether a cursor may move. What it noticed is kept with
	// the cursor in one write when the records are Stored, kept alone
	// when they are Paused, and dropped otherwise: the same lines are
	// read again, and noticed again, by the read that stores them.
	Store func(ReadResult) (Storing, drift.Signals)
	// ReadAt is the time this run records on every cursor it writes. A
	// read that finds nothing new writes no cursor. The zero time leaves
	// the entry's own time as it was.
	ReadAt time.Time
}

// Advance applies the whole rule to the entry registered for path:
// read what the file gained since its cursor, hand the result to
// Store, and persist the advanced cursor only once Store keeps every
// record. A cursor written past a record that nothing stored would
// lose that record for good, which is why the order is never the other
// way. A file that gained more than one segment holds is read in
// rounds of one segment each, until nothing is left or a round does
// not move the cursor.
//
// Each round runs under the entry's lock, and the cursor it starts
// from is the one the registry holds once the lock is taken, never one
// the caller or an earlier round read before. Two reads from one
// cursor make two records with one id; the store keeps the first, and
// were the second one longer, the cursor it writes would pass lines
// that only the discarded one held. So a round that reads a segment
// writes the segment's bound into the entry before it asks the store,
// and a round that finds a bound left for its cursor — the cursor write failed, or
// the reader died before it — reads the same segment again and
// nothing past it. An entry another reader holds is
// left to it: its cursor stays where that reader puts it. A holder
// that a reader in this process gave up on reads one round more once
// it lets go, so lines written before that reader left are read by the
// time the holder's Advance returns. Nothing marks a holder that a
// reader in another process gave up on: what that reader came for
// waits for the next run. The lock is taken again for every round, so
// what one holder may keep it for is one segment, not one file — and,
// in a round from byte 0 that meets the segment bound before any
// relocated line, one pass over the rest of the file to find one.
//
// A round writes its own entry and nothing else, and only the reader
// that holds an entry writes it. The one round that takes the
// project's lock is the one that removes its entry, which keeps what
// the entry noticed in the project file, and takes the heat and the
// told mark of its session when it was the session's last entry.
//
// An entry whose file is gone for good, as gone judges it, leaves the
// registry: its cursor can never advance again and no search finds the
// file to register it a second time. An entry whose file is only out
// of reach stays as it is. An entry whose session moved out of the directories
// consent covers stays, and is read on: its cursor moves past what the
// session writes outside, and what it writes after it comes back is
// kept again. An agent file is outside while its session's main file
// is: only the main file holds the lines that say where the session
// went.
//
// Advance reports whether the project's next entry may be read.
func (rd Reader) Advance(path string) bool {
	for {
		next, more := rd.round(path)
		if !more {
			return next
		}
	}
}

// round is one round of Advance: at most one segment read, stored, and
// written back. next is what Advance reports; more reports that lines
// past what the round read wait: the cursor moved to a segment's
// bound, or a reader in this process gave up waiting for the entry
// while the round held it, and the file may have grown past what the
// round observed.
func (rd Reader) round(path string) (next, more bool) {
	held, ok := rd.Registry.lockEntry(rd.ProjectIDHash, path, entryLockWait)
	if !ok {
		return true, false
	}
	defer func() {
		if held.unlock() && next {
			more = true
		}
	}()
	return rd.roundHeld(held)
}

// roundHeld is the part of round that runs under the entry's lock. It
// writes the entry at most twice: the bound of the segment it read
// before the store is asked, and the cursor past it once the store
// kept it.
func (rd Reader) roundHeld(held *heldEntry) (next, more bool) {
	cur, ok, err := held.read()
	if err != nil || !ok || !cur.readable() {
		return true, false
	}
	f := cur.File
	capture := rd.Capture
	capture.ProjectSubpath = f.Subpath
	// The agent file is observed before its main file is looked at. A
	// move is written to the main file before anything the session
	// writes after it, so a move out that precedes any byte this read
	// takes is already on disk when the main file is looked at, whether
	// or not a reader consumed it yet, and in whatever order the
	// session's files are read.
	st, err := StatFile(f.Path)
	if err != nil {
		return true, false
	}
	if !st.Exists && !gone(f.Path) {
		// The file is out of reach rather than gone for good, and its
		// entry waits for it as it is.
		return true, false
	}
	outside, err := rd.sessionOutside(f.Path)
	if err != nil {
		return true, false
	}
	left, stands := cur.standingBound()
	again := stands && left.holds(st)
	var res ReadResult
	switch {
	case again:
		res, err = left.readAgain(f, st, capture, rd.Options)
	case stands:
		// The store may hold a segment under the index the bound
		// names, read from bytes the file no longer holds. This read
		// is not that segment, so it takes the index after it.
		from := f
		from.NextSegment = left.Segment + 1
		res, err = readObserved(from, st, capture, rd.Options, outside)
	default:
		res, err = readObserved(f, st, capture, rd.Options, outside)
	}
	if err != nil {
		// A file that could not be read this time — a metadata file
		// caught mid-write, say — keeps its cursor and is read again
		// in the next run. The project's other files still make
		// progress.
		return true, false
	}
	bound, bounded := boundOf(f, st, res)
	if bounded && (!stands || bound != left) {
		if err := held.keepBound(bound); err != nil {
			// A segment whose bound is not on disk is not handed to
			// the store: nothing would tell the next read where it
			// ended.
			return true, false
		}
	}
	storing, noticed := rd.Store(res)
	switch storing {
	case Paused:
		if noticed.Any() {
			_ = held.notice(noticed)
		}
		return false, false
	case Held:
		return false, false
	case NotStored:
		return true, false
	case Stored:
	}
	if res.Reaction == Vanished {
		_, _ = rd.Registry.leave(rd.ProjectIDHash, []*heldEntry{held})
		return true, false
	}
	if !f.changedBy(res.File) && !noticed.Any() {
		// The entry already says everything this read found. Writing it
		// anyway would rewrite one entry file each time a session's
		// files are looked at, and most of them gain nothing.
		return true, false
	}
	// The cursor carries when a read last found something new, so a
	// surface can say so without a clock of its own.
	if !rd.ReadAt.IsZero() {
		res.File.ReadAt = rd.ReadAt.UTC().Format(readAtLayout)
	}
	if err := held.advance(f, res.File, noticed); err != nil {
		return true, false
	}
	// A segment read again ends where it ended before, and what the
	// file gained since waits past it.
	return true, res.More || (again && st.Size > left.End)
}

// errSessionUnread reports an agent file whose main file has an entry
// this build may not read.
var errSessionUnread = errors.New("follow: the entry of the session's main file needs a later build")

// sessionOutside reports whether the session the agent file at path
// belongs to is outside, or was since its main file's cursor, as
// sessionWasOutside answers it. A main file, and an agent file whose
// main file is not registered, answer for themselves. An agent file
// whose main file has an entry this build cannot or may not read has
// no answer, and is not read: whether its session is outside is in
// that entry alone.
func (rd Reader) sessionOutside(path string) (bool, error) {
	if _, file := identify(path); file == "" {
		return false, nil
	}
	main, ok, err := rd.Registry.readEntry(rd.ProjectIDHash, SessionOf(path)+linesExt)
	if err != nil || !ok {
		return false, err
	}
	if !main.readable() {
		return false, errSessionUnread
	}
	return sessionWasOutside(main.File, rd.Options)
}
