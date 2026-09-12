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
	Capture envelope.TranscriptCapture
	// Store lands the records of one read and reports what became of
	// them. It is the one judge of whether a cursor may move.
	Store func(ReadResult) Storing
	// ReadAt is the time this run records on every cursor it moves.
	// The zero time leaves the entry's own time as it was.
	ReadAt time.Time
}

// Advance applies the whole rule to one registered entry: read what
// the file gained since its cursor, hand the result to Store, and
// persist the advanced cursor only once Store keeps every record. A
// cursor written past a record that nothing stored would lose that
// record for good, which is why the order is never the other way.
//
// An entry whose file vanished leaves the registry: its cursor can
// never advance again and no search finds the file to register it a
// second time. An entry whose session moved out of the directories
// consent covers stays, retired: the file is still on disk, and the
// retirement is what keeps a later registration from reading it from
// its start and sending again what was sent already.
//
// Advance reports whether the project's next entry may be read.
func (rd Reader) Advance(f File) bool {
	capture := rd.Capture
	capture.ProjectSubpath = f.Subpath
	res, err := Read(f, capture, rd.Options)
	if err != nil {
		// A file that could not be read this time — a metadata file
		// caught mid-write, say — keeps its cursor and is read again
		// in the next run. The project's other files still make
		// progress.
		return true
	}
	switch rd.Store(res) {
	case Held:
		return false
	case NotStored:
		return true
	}
	switch {
	case res.Reaction == Vanished:
		_ = rd.Registry.Remove(rd.ProjectIDHash, f.Path)
	case res.Stopped:
		_ = rd.Registry.Retire(rd.ProjectIDHash, res.File, Relocated)
	default:
		// The cursor carries when it was last moved, so a surface can
		// say when a file was last read without a clock of its own.
		if !rd.ReadAt.IsZero() {
			res.File.ReadAt = rd.ReadAt.UTC().Format(readAtLayout)
		}
		_ = rd.Registry.Update(rd.ProjectIDHash, res.File)
	}
	return true
}
