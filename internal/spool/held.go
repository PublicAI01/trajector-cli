package spool

import (
	"os"
	"path/filepath"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

// heldDirName is the directory of the third slot, beside the rawcall
// days and the records slot:
//
//	<dir>/records-held/<YYYYMMDD>/<record_id>.json
//
// It holds records this build read but must not upload, because a
// field of theirs names where the session ran in a shape this build
// cannot mask. They are the user's data, kept as read, and nothing
// that prepares a batch ever looks here: the slot has no reader that
// leads to an upload, which is what makes "held on this machine" a
// property of where the file is rather than of a flag someone must
// check.
//
// The slot carries no sidecar index. It holds few records and is read
// only by the surfaces that count them and by doctor, so each record
// is attributed from its own bytes; every day directory is still named
// the way the other slots name theirs.
//
// Nor does it count against the quota. The records here leave only
// when a later build can mask them or the user deletes them, so a
// device that keeps meeting a shape it cannot read would otherwise
// fill the quota with bytes nothing can drain and stop recording
// altogether.
const heldDirName = "records-held"

// Hold stores one record in the held slot, under the record id its own
// bytes declare. The bytes are stored as they were read: a record that
// may not leave the device is never rewritten on its way to disk.
// Storage is idempotent per record id, and it goes through the same
// write sequence and the same refusals as the records slot's: a record
// that cannot be addressed afterwards must not be stored where only a
// later build can take it out.
func (s *Spool) Hold(data []byte) error {
	return s.storeRecord(heldSlot, data, envelope.Kind{})
}

// Held lists every record in the held slot, oldest day first, each
// with its own bytes and its size. A record that cannot be attributed
// still comes back, described by its id alone, so nothing in the slot
// is invisible to a reader; and because the sizes come back with the
// records, what the slot occupies is counted where the records are
// counted and never walked a second time.
func (s *Spool) Held() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heldRecords()
}

func (s *Spool) heldRecords() ([]Record, error) {
	var held []Record
	days, err := s.slotDays(heldSlot)
	if err != nil {
		return nil, err
	}
	for _, dayDir := range days {
		files, err := recordFiles(dayDir)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			data, present, err := openRecordFile(f.path)
			if err != nil {
				return nil, err
			}
			if !present {
				continue
			}
			r := describeRecord(f.id, data)
			if r.Timestamp.IsZero() {
				r.Timestamp = f.mod
			}
			r.Size, r.Raw = int64(len(data)), data
			held = append(held, r)
		}
	}
	return held, nil
}

// Release moves one held record into the records slot, where the next
// batch picks it up. It is how a build that can mask a shape an
// earlier one could not sends what the earlier one kept back. The
// record is written first and removed from the held slot only once it
// is stored, so an interruption repeats a record rather than losing
// one; storing is idempotent by id, and the repeat costs nothing.
func (s *Spool) Release(recordID string) error {
	s.mu.Lock()
	days, err := s.slotDays(heldSlot)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	path := ""
	for _, dayDir := range days {
		candidate := filepath.Join(dayDir, recordID+".json")
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			break
		}
	}
	if path == "" {
		s.mu.Unlock()
		return os.ErrNotExist
	}
	data, present, err := openRecordFile(path)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	if err := s.WriteRecord(data); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(path); err != nil && !vanished(err) {
		return err
	}
	// Only the write above changed what the quota counts: the held
	// slot is charged nothing, so removing the record here frees
	// nothing either.
	s.sig = dirSignature(s.dir)
	return nil
}

// deleteHeldLocked removes every held record the matcher accepts.
// Callers hold s.mu and refresh the signature afterwards.
func (s *Spool) deleteHeldLocked(match func(Record) bool) (int, error) {
	held, err := s.heldRecords()
	if err != nil {
		return 0, err
	}
	days, err := s.slotDays(heldSlot)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, r := range held {
		if !match(r) {
			continue
		}
		for _, dayDir := range days {
			path := filepath.Join(dayDir, r.ID+".json")
			if _, err := os.Stat(path); err != nil {
				continue
			}
			if err := os.Remove(path); err != nil && !vanished(err) {
				return deleted, err
			}
			deleted++
			break
		}
	}
	return deleted, nil
}
