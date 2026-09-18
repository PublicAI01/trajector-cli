package spool

import (
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

// Entry is one stored record as a caller that takes records out of the
// spool sees it: what the record declares itself to be, the id it is
// addressed by, and its bytes. Records of every kind read back as an
// Entry, so such a caller iterates, counts and deletes records once
// rather than once per kind.
//
// Kind names the slot as well as the record. The source decides which
// of the two directories holds the record, so an entry addresses
// itself: an id that happens to be spelled the same in both slots
// still names one entry. A record whose bytes name no kind keeps the
// source of the slot it sits in, or it could never be deleted.
type Entry struct {
	Kind envelope.Kind
	ID   string
	// SessionKey groups the rawcalls of one coding session. It comes
	// from the day index, and is empty both for a rawcall the index
	// missed and for every record of the second slot, which carries its
	// session inside its own bytes.
	SessionKey string
	Timestamp  time.Time
	Raw        []byte
}

// Entries is a set of stored records, of any kind and from either
// slot.
type Entries []Entry

// entry reads one rawcall as a stored record.
func (r Rawcall) entry() Entry {
	return Entry{
		Kind:       envelope.KindRawcall,
		ID:         r.RequestID,
		SessionKey: r.SessionKey,
		Timestamp:  r.Timestamp,
		Raw:        r.Data,
	}
}

// entry reads one second-slot record as a stored record.
func (r Record) entry() Entry {
	return Entry{Kind: recordKind(r.Kind), ID: r.ID, Timestamp: r.Timestamp, Raw: r.Raw}
}

// recordKind is the whole kind of a second-slot record: what the record
// says it is, and the source that kind belongs to. A record whose bytes
// name no kind keeps a source of the slot all the same, because which
// slot holds a record is known even when its content is not — without
// one it could never be deleted.
func recordKind(kind string) envelope.Kind {
	if kind == envelope.KindGitSnapshot.RecordKind {
		return envelope.KindGitSnapshot
	}
	return envelope.Kind{Source: envelope.KindSegment.Source, RecordKind: kind}
}

// inRawcallSlot reports which of the two directories holds an entry.
// It is the only mapping from a record's kind to its slot.
func (e Entry) inRawcallSlot() bool { return e.Kind.Source == envelope.KindRawcall.Source }

// EachEntry visits every stored record of every kind, stopping at the
// first error the visitor returns: the rawcalls oldest day first, then
// the second slot. The two directories are how records are stored, not
// what they are, so they arrive as one sequence.
func (s *Spool) EachEntry(visit func(Entry) error) error {
	return s.EachEntryWhere(func(string) bool { return true }, visit)
}

// EachEntryWhere is EachEntry restricted to the records whose id
// matches. Only a matching record's bytes are read, in either slot: the
// id is the file name, so selecting on it costs a directory listing
// rather than a read of every record on the machine.
//
// An id names one entry per slot, so a caller that cares which slot a
// record came from still settles that on the Entry itself; the match is
// the cheap half, and is allowed to be a superset.
//
// resendPending is why this exists, and it read every stored record
// until 2026-09-14. It looks for at most one batch's worth, so every
// automatic flush that met a standing pending lease re-read the whole
// spool: up to the entire quota, once a minute, for as long as the
// lease stood. That is not a corner — an offline machine fails its
// uploads with a network error, which sets no pause at all, so the
// lease stands and the cadence keeps its full minute rate. The same
// scan also made Uploader.Close's budget unenforceable, since the
// deadline is first consulted after it, and let one unreadable record
// anywhere in the spool block the resend of every pending batch for
// good.
func (s *Spool) EachEntryWhere(match func(id string) bool, visit func(Entry) error) error {
	if err := s.EachWhere(match, func(r Rawcall) error { return visit(r.entry()) }); err != nil {
		return err
	}
	return s.EachRecordWhere(match, func(r Record) error { return visit(r.entry()) })
}

// DeleteEntries removes the named records, each from the slot its own
// kind names. An id spelled the same in both slots is deleted only
// from the slot the entry named, so deleting one record never takes
// another record with it.
func (s *Spool) DeleteEntries(entries Entries) error {
	rawcalls, records := map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		if e.inRawcallSlot() {
			rawcalls[e.ID] = true
			continue
		}
		records[e.ID] = true
	}
	if len(rawcalls) > 0 {
		if _, err := s.DeleteWhere(func(id string) bool { return rawcalls[id] }); err != nil {
			return err
		}
	}
	if len(records) > 0 {
		if _, err := s.DeleteRecordsWhere(func(r Record) bool { return records[r.ID] }); err != nil {
			return err
		}
	}
	return nil
}

// OldestEntry reports when the oldest record still waiting in the
// spool was captured, of whichever kind that is, and false when the
// spool holds none.
func (s *Spool) OldestEntry() (time.Time, bool) {
	oldest, found := s.Oldest()
	if at, ok := s.OldestRecord(); ok && (!found || at.Before(oldest)) {
		oldest, found = at, true
	}
	return oldest, found
}
