// Package sessionread reads an enabled project's registered session
// files into the spool. It is the one rule both processes that read
// share — the one-shot process a session hook starts and the resident
// proxy reading on a hook's word — so the two cannot disagree about
// what a read stores, what holds it, or when a cursor moves.
package sessionread

import (
	"errors"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/follow"
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
	// Now is the record-keeping clock.
	Now func() time.Time
}

// Read hands every file of p to one follow.Reader, in the order
// given, and stores what it consumes. A project the routing table does
// not clear is nothing to read. A failure to read one file is left
// behind so the next file, and the next run, still make progress; a
// condition that holds for every file alike stops the run. Read
// reports whether every file was offered.
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
		Store:  func(res follow.ReadResult) follow.Storing { return rd.store(p.Hash, res) },
		ReadAt: now,
	}
	for _, f := range files {
		if !reader.Advance(f) {
			return false
		}
	}
	return true
}

// store holds what one read produced against the shape this build
// masks and reads by, and then lands its records in the spool. It is
// the whole of what storing means here, and the reader moves a cursor
// only on the answer it gives.
func (rd Reader) store(projectIDHash string, res follow.ReadResult) follow.Storing {
	hold, err := rd.inspectSegments(projectIDHash, res.Segments)
	if err != nil {
		return follow.NotStored
	}
	if hold {
		// Nothing of this read is stored: the same lines are met again
		// by whichever build reads next, and only one that can mask
		// them may store them.
		return follow.Held
	}
	full, err := storeRecords(rd.Spool, res)
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
// part of it the scan calls unexpected. Lines this build cannot mask
// hold the run and pause recording device-wide until a different build
// reads them; everything else is counted and reading goes on. This is
// the one place a line's fields are read before the spool, so it is
// where the shape is checked; the reader itself interprets nothing,
// and what a finding is called is stated where the scan happens, not
// here. A registry or a log that cannot be written is let go: what
// was noticed is worth keeping and never worth stopping a read for.
func (rd Reader) inspectSegments(projectIDHash string, segments []envelope.Segment) (hold bool, err error) {
	for _, seg := range segments {
		found, err := drift.Scan([]byte(seg.Lines))
		if err != nil {
			return false, err
		}
		if !found.Any() {
			continue
		}
		_ = rd.Registry.AddSignals(projectIDHash, found)
		if found.Unexpected() {
			_ = drift.AppendLog(rd.ReaderLog, rd.Now().UTC().Format(time.RFC3339), projectIDHash, found)
		}
		if found.Stop() {
			_ = rd.Routes.PauseByBuild(routing.PauseRedactionDrift, rd.Version)
			return true, nil
		}
	}
	return false, nil
}

// storeRecords writes a read result's records to the spool. full
// reports that the spool refused a record for want of room, the one
// outcome that must stop the whole run rather than advance a cursor
// past records that were never stored. One pass answers for every
// record a read produced, so that outcome is decided once and not once
// per kind.
func storeRecords(sp *spool.Spool, res follow.ReadResult) (full bool, err error) {
	for _, write := range recordWrites(sp, res) {
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
// the spool method its own kind names.
func recordWrites(sp *spool.Spool, res follow.ReadResult) []func() error {
	writes := make([]func() error, 0, len(res.Segments)+len(res.Snapshots))
	for _, seg := range res.Segments {
		writes = append(writes, func() error { return sp.WriteSegment(seg) })
	}
	for _, snap := range res.Snapshots {
		writes = append(writes, func() error { return sp.WriteMetaSnapshot(snap) })
	}
	return writes
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
