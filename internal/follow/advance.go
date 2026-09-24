package follow

import (
	"time"

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
	// the answer for a condition that holds for every entry alike —
	// no room left to store in, or content that this build must not
	// store.
	Held
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
	// them. It is the one judge of whether a cursor may move.
	Store func(ReadResult) Storing
	// ReadAt is the time this run records on every cursor it moves.
	// The zero time leaves the entry's own time as it was.
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
// cursor make two records with one id and different lines; the store
// keeps the first, and the cursor the second one writes can pass lines
// that only the discarded one held. An entry another reader holds is
// left to it: its cursor stays where that reader puts it, and the next
// run reads what is left. The lock is taken again for every round, so
// what one holder may keep it for is one segment, not one file — and,
// in a round from byte 0 that meets the segment bound before any
// relocated line, one pass over the rest of the file to find one.
//
// An entry whose file vanished leaves the registry: its cursor can
// never advance again and no search finds the file to register it a
// second time. An entry whose session moved out of the directories
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
// written back. next is what Advance reports; more reports that the
// cursor moved to a segment's bound and lines past it wait.
func (rd Reader) round(path string) (next, more bool) {
	unlock, ok := rd.Registry.lockEntry(rd.ProjectIDHash, path, entryLockWait)
	if !ok {
		return true, false
	}
	defer unlock()
	f, ok, err := rd.Registry.entry(rd.ProjectIDHash, path)
	if err != nil || !ok {
		return true, false
	}
	if f.Retired != "" {
		return true, false
	}
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
	outside, err := rd.sessionOutside(f.Path)
	if err != nil {
		return true, false
	}
	res, err := readObserved(f, st, capture, rd.Options, outside)
	if err != nil {
		// A file that could not be read this time — a metadata file
		// caught mid-write, say — keeps its cursor and is read again
		// in the next run. The project's other files still make
		// progress.
		return true, false
	}
	switch rd.Store(res) {
	case Held:
		return false, false
	case NotStored:
		return true, false
	}
	switch {
	case res.Reaction == Vanished:
		_ = rd.Registry.Remove(rd.ProjectIDHash, f.Path)
		return true, false
	}
	// The cursor carries when it was last moved, so a surface can say
	// when a file was last read without a clock of its own.
	if !rd.ReadAt.IsZero() {
		res.File.ReadAt = rd.ReadAt.UTC().Format(readAtLayout)
	}
	if err := rd.Registry.Update(rd.ProjectIDHash, f, res.File); err != nil {
		return true, false
	}
	return true, res.More
}

// sessionOutside reports whether the session the agent file at path
// belongs to is outside, or was since its main file's cursor, as
// sessionWasOutside answers it. A main file, and an agent file whose
// main file is not registered, answer for themselves.
func (rd Reader) sessionOutside(path string) (bool, error) {
	if _, file := identify(path); file == "" {
		return false, nil
	}
	main, ok, err := rd.Registry.entry(rd.ProjectIDHash, SessionOf(path)+linesExt)
	if err != nil || !ok {
		return false, err
	}
	return sessionWasOutside(main, rd.Options)
}
