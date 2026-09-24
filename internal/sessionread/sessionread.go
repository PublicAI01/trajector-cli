// Package sessionread reads an enabled project's registered session
// files into the spool. It is the one rule both processes that read
// share — the one-shot process a session hook starts and the resident
// proxy reading on a hook's word — so the two cannot disagree about
// what a read stores, what holds it, or when a cursor moves.
//
// Releasing is the same question asked later, so it is asked here too:
// a record held back from upload leaves the machine only once this
// rule reads its lines and keeps nothing back. A second reader with a
// judgement of its own is how a record held for a shape this build
// cannot mask would be uploaded unmasked.
package sessionread

import (
	"errors"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/redact"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// Project is what a read needs to know about the project the files
// belong to: the grant's token, hash, root, and shape.
type Project struct {
	Token string
	Hash  string
	Root  string
	Shape routing.Shape
}

// ProjectOf is the project a grant describes.
func ProjectOf(g routing.Grant) Project {
	return Project{Token: g.Token, Hash: g.ProjectIDHash, Root: g.RootPath, Shape: g.Shape}
}

// Reader reads registered files for one device. Every field is a
// store of the device or a fact about this build; nothing here is
// per read.
type Reader struct {
	Registry *follow.Registry
	Routes   *routing.Store
	Spool    *spool.Spool
	// Version is this build, stated on every record and named on a
	// pause this build has to write.
	Version string
	// ReaderLog is where a line shape this build did not expect is
	// noted.
	ReaderLog string
	// Home is the user's home directory, the user profile directory on
	// Windows. With the project root it says where a session's own
	// location is, which is what a field naming a path is held
	// against.
	Home string
	// Now is the record-keeping clock.
	Now func() time.Time
}

// Read hands every file of p to one follow.Reader, in the order
// given, and stores what it consumes. A project the routing table does
// not clear is nothing to read. A failure to read one file is left
// behind so the next file, and the next run, still make progress; a
// condition that holds for every file alike stops the run. Only the
// path of each file is taken: a read starts from the cursor the
// registry holds when the read begins. Read reports whether every
// file was offered.
func (rd Reader) Read(p Project, files []follow.File) bool {
	if !rd.Routes.Records(p.Token) {
		return false
	}
	now := rd.Now()
	reader := follow.Reader{
		Registry:      rd.Registry,
		ProjectIDHash: p.Hash,
		Options:       follow.ReadOptions{Root: p.Root},
		Capture: envelope.Capture{
			ClientVersion: rd.Version,
			Timestamp:     now.UTC().Format(time.RFC3339Nano),
			ProjectIDHash: p.Hash,
			Injection:     InjectionValue(p.Shape),
		},
		Store:  func(res follow.ReadResult) follow.Storing { return rd.store(p, res) },
		ReadAt: now,
	}
	for _, f := range files {
		if !reader.Advance(f.Path) {
			return false
		}
	}
	return true
}

// Stops reports whether the files of p hold, past their cursors,
// anything after which no read may store: the finding that pauses
// recording device-wide. It stores nothing, moves no cursor, and
// writes neither the registry nor the log — it only asks this build's
// own question about lines a pause was set over. A file that cannot
// be read answers nothing, because a pause is lifted on what was
// read and never on what could not be.
func (rd Reader) Stops(p Project, files []follow.File) bool {
	location := rd.location(p)
	capture := envelope.Capture{
		ClientVersion: rd.Version,
		Timestamp:     rd.Now().UTC().Format(time.RFC3339Nano),
		ProjectIDHash: p.Hash,
		Injection:     InjectionValue(p.Shape),
	}
	for _, f := range files {
		if stopsIn(f, capture, location, follow.ReadOptions{Root: p.Root}) {
			return true
		}
	}
	return false
}

// stopsIn asks Stops' question of one file, segment after segment, to
// the end of what the file holds past its cursor.
func stopsIn(f follow.File, capture envelope.Capture, location redact.SessionLocation, opts follow.ReadOptions) bool {
	for {
		res, err := follow.Read(f, capture, opts)
		if err != nil {
			return false
		}
		for _, seg := range res.Segments {
			found, err := drift.Scan([]byte(seg.Lines), location)
			if err != nil || found.Stop() {
				return true
			}
		}
		if !res.More {
			return false
		}
		f = res.File
	}
}

// store holds what one read produced against the shape this build
// masks and reads by, and then lands its records in the spool. It is
// the whole of what storing means here, and the reader moves a cursor
// only on the answer it gives.
func (rd Reader) store(p Project, res follow.ReadResult) follow.Storing {
	pause, held, err := rd.inspectSegments(p, res.Segments)
	if err != nil {
		return follow.NotStored
	}
	if pause {
		// Nothing of this read is stored: the same lines are met again
		// by whichever build reads next, and only one that can mask
		// them may store them.
		return follow.Held
	}
	full, err := storeRecords(rd.Spool, res, held)
	switch {
	case full:
		// The spool is full: it dropped nothing, and neither does the
		// reader. This file and every one after it is read again once
		// space returns; no record is repeated, because storing is
		// idempotent by id.
		return follow.Held
	case err != nil:
		return follow.NotStored
	}
	return follow.Stored
}

// inspectSegments holds each segment's lines against the shape this
// build masks and reads by, before anything is stored, and keeps what
// it noticed with the project's registry, and in the reader log the
// part of it the scan calls unexpected. It reports the two outcomes a
// finding can have. A reader that contradicted itself pauses the run
// and recording device-wide until a different build reads the files:
// nothing it handed over can be trusted. A segment carrying a field
// this build cannot mask is named in held: that one segment stays on
// this machine, and every other segment of the read is stored and
// uploaded as usual. This is the one place a line's fields are read
// before the spool, so it is where the shape is checked; the reader
// itself interprets nothing, and what a finding is called is stated
// where the scan happens, not here. A registry or a log that cannot be
// written is let go: what was noticed is worth keeping and never worth
// stopping a read for.
func (rd Reader) inspectSegments(p Project, segments []envelope.Segment) (pause bool, held map[string]bool, err error) {
	location := rd.location(p)
	for _, seg := range segments {
		found, err := drift.Scan([]byte(seg.Lines), location)
		if err != nil {
			return false, nil, err
		}
		if !found.Any() {
			continue
		}
		_ = rd.Registry.AddSignals(p.Hash, found)
		if found.Unexpected() {
			_ = drift.AppendLog(rd.ReaderLog, rd.Now().UTC().Format(time.RFC3339), p.Hash, found)
		}
		if found.Stop() {
			_ = rd.Routes.PauseByBuild(routing.PauseRedactionDrift, rd.Version)
			return true, nil, nil
		}
		if keepsHere(found) {
			if held == nil {
				held = map[string]bool{}
			}
			held[seg.RecordID] = true
		}
	}
	return false, held, nil
}

// location is where the sessions of p ran, as every reading of their
// lines must state it: the user's home directory and the project's own
// root. It is built in one place because a reading that states less
// than another lets through what the other would keep back — a field
// naming a path is held against this value, and a location missed is a
// location uploaded.
func (rd Reader) location(p Project) redact.SessionLocation {
	return redact.SessionLocation{Home: rd.Home, Project: p.Root}
}

// keepsHere reports that a scan's findings put the segment they were
// made over on this machine. It is the one predicate both the reading
// that holds a segment back and the run that offers to release it ask,
// so a segment can never be released by a question weaker than the one
// that held it.
func keepsHere(found drift.Signals) bool { return found.Quarantine() || found.Stop() }

// ReleaseHeld reads held records of p through this build's detector
// again and moves the ones it keeps nothing back from into the slot a
// batch reads. A build whose anchored list covers a shape an earlier
// build did not is how a held record reaches the service; a record
// this build still cannot mask stays where it is, and nothing is ever
// rewritten to make it pass.
//
// It reports how many records it moved, and stops at the first record
// the spool refuses: a refusal is the spool's state and not the
// record's, so the records after it would be refused too.
func (rd Reader) ReleaseHeld(p Project, held []spool.Record) (released int, err error) {
	location := rd.location(p)
	for _, r := range held {
		seg, err := envelope.ParseSegment(r.Raw)
		if err != nil {
			continue
		}
		found, err := drift.Scan([]byte(seg.Lines), location)
		if err != nil || keepsHere(found) {
			continue
		}
		if err := rd.Spool.Release(r.ID); err != nil {
			return released, err
		}
		released++
	}
	return released, nil
}

// storeRecords writes a read result's records to the spool. full
// reports that the spool refused a record for want of room, the one
// outcome that must stop the whole run rather than advance a cursor
// past records that were never stored. One pass answers for every
// record a read produced, so that outcome is decided once and not once
// per kind.
func storeRecords(sp *spool.Spool, res follow.ReadResult, held map[string]bool) (full bool, err error) {
	for _, write := range recordWrites(sp, res, held) {
		if err := write(); err != nil {
			if errors.Is(err, spool.ErrQuotaExceeded) {
				return true, nil
			}
			return false, err
		}
	}
	return false, nil
}

// recordWrites is the writes one read result asks of the spool, in the
// order it asks for them. It is the one place that pairs a record with
// the spool method its own kind names, and the one place that sends a
// held segment to the slot nothing uploads.
func recordWrites(sp *spool.Spool, res follow.ReadResult, held map[string]bool) []func() error {
	writes := make([]func() error, 0, len(res.Segments)+len(res.Snapshots))
	for _, seg := range res.Segments {
		if held[seg.RecordID] {
			// The held slot is not a slot a batch reads, so a segment
			// this build cannot mask stays on the machine while the
			// cursor moves past it like any other.
			writes = append(writes, func() error { return holdSegment(sp, seg) })
			continue
		}
		writes = append(writes, func() error { return sp.WriteSegment(seg) })
	}
	for _, snap := range res.Snapshots {
		writes = append(writes, func() error { return sp.WriteMetaSnapshot(snap) })
	}
	return writes
}

// holdSegment stores one segment where nothing uploads it.
func holdSegment(sp *spool.Spool, seg envelope.Segment) error {
	data, err := seg.Bytes()
	if err != nil {
		return err
	}
	return sp.Hold(data)
}

// InjectionValue names, for a record, what this client did with the
// project's traffic: forwarded it, or only read the files it left. It
// is the wire spelling of the shape the grant records.
func InjectionValue(shape routing.Shape) string {
	if shape == routing.WithoutProxy {
		return envelope.InjectionTailOnly
	}
	return envelope.InjectionProxy
}
