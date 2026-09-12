package spool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// recordsDirName is the subdirectory holding the second slot: segment
// and snapshot records. It sits beside the rawcall day directories, so
// rawcall readers must skip it by name and record readers start there.
const recordsDirName = "records"

// recordIndexLine is one entry in a record day's sidecar index. Every
// field is copied from the record at write time, so the index can only
// ever restate what the file says; a lost index is rebuilt by reading
// the files. Size is kept so a caller reading the index alone can
// budget a batch without a stat per file.
type recordIndexLine struct {
	RecordID      string `json:"record_id"`
	RecordKind    string `json:"record_kind"`
	SessionID     string `json:"session_id"`
	ProjectIDHash string `json:"project_id_hash,omitempty"`
	Size          int64  `json:"size"`
	Timestamp     string `json:"timestamp"`
}

// Record is one stored segment or snapshot as a reader sees it. Kind,
// SessionID, ProjectIDHash and Timestamp come from the day index; a
// record the index does not account for is attributed from its own
// bytes, and one that cannot be parsed reads back with only its id, the
// file's modification time and its raw bytes, for the caller to set
// aside. Raw is nil where the record was addressed without being read.
type Record struct {
	ID            string
	Kind          string
	SessionID     string
	ProjectIDHash string
	Timestamp     time.Time
	Raw           []byte
}

// WriteSegment stores one segment. Storage is idempotent per record id:
// a segment already on disk is left untouched and the write reports
// success, because the id is derived from the segment's identity and
// the same segment resent after a crash must not cost a rewrite.
func (s *Spool) WriteSegment(seg envelope.Segment) error {
	kind := envelope.Kind{Source: seg.Source, RecordKind: seg.RecordKind}
	if kind != envelope.KindSegment {
		return fmt.Errorf("spool: record %s declares %s/%s, not a segment", seg.RecordID, seg.Source, seg.RecordKind)
	}
	data, err := seg.Bytes()
	if err != nil {
		return err
	}
	return s.writeRecord(seg.RecordID, seg.RecordKind, seg.SessionID, seg.Capture, data)
}

// WriteMetaSnapshot stores one snapshot. A snapshot's id changes with
// its content, so an unchanged snapshot resent is the idempotent case
// and a changed one is simply a new record: which snapshot is current
// is the producer's knowledge, not the spool's.
func (s *Spool) WriteMetaSnapshot(snap envelope.MetaSnapshot) error {
	kind := envelope.Kind{Source: snap.Source, RecordKind: snap.RecordKind}
	if kind != envelope.KindMetaSnapshot {
		return fmt.Errorf("spool: record %s declares %s/%s, not a snapshot", snap.RecordID, snap.Source, snap.RecordKind)
	}
	data, err := snap.Bytes()
	if err != nil {
		return err
	}
	return s.writeRecord(snap.RecordID, snap.RecordKind, snap.SessionID, snap.Capture, data)
}

func (s *Spool) writeRecord(id, kind, sessionID string, capture envelope.TranscriptCapture, data []byte) error {
	// The spool builds a file path from the id and must not trust it.
	// Record ids and request ids share one shape rule so neither can
	// name a file outside its day directory.
	if !envelope.ValidRequestID(id) {
		return fmt.Errorf("spool: invalid record id %q", id)
	}
	// A record with no session cannot be addressed for deletion by the
	// session it came from, so it must not be stored.
	if sessionID == "" {
		return fmt.Errorf("spool: record %s carries no session id", id)
	}
	at, err := time.Parse(time.RFC3339Nano, capture.Timestamp)
	if err != nil || at.IsZero() {
		return fmt.Errorf("spool: record %s carries no capture timestamp", id)
	}
	line, err := json.Marshal(recordIndexLine{
		RecordID:      id,
		RecordKind:    kind,
		SessionID:     sessionID,
		ProjectIDHash: capture.ProjectIDHash,
		Size:          int64(len(data)),
		Timestamp:     at.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()

	// The same id resent later may carry a later capture timestamp and
	// so name a different day, so existence is checked across every
	// record day rather than only the one this write would land in.
	if s.recordExistsLocked(id) {
		return nil
	}
	if s.wouldExceedLocked(int64(len(data)) + int64(len(line))) {
		return ErrQuotaExceeded
	}

	dayDir := filepath.Join(s.dir, recordsDirName, at.UTC().Format(dayLayout))
	if err := os.MkdirAll(dayDir, 0o700); err != nil {
		return err
	}
	if err := fsatomic.WriteFile(filepath.Join(dayDir, id+".json"), data, 0o600); err != nil {
		return err
	}
	s.usage += int64(len(data))

	if err := appendLine(filepath.Join(dayDir, indexName), line); err != nil {
		return err
	}
	s.usage += int64(len(line))
	s.sig = dirSignature(s.dir)
	return nil
}

func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func (s *Spool) recordExistsLocked(id string) bool {
	days, err := s.recordDays()
	if err != nil {
		return false
	}
	for _, dayDir := range days {
		if _, err := os.Stat(filepath.Join(dayDir, id+".json")); err == nil {
			return true
		}
	}
	return false
}

// recordDays lists the record day directories, oldest first. A spool
// that never stored a record has none.
func (s *Spool) recordDays() ([]string, error) {
	root := filepath.Join(s.dir, recordsDirName)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var days []string
	for _, e := range entries {
		if e.IsDir() {
			days = append(days, filepath.Join(root, e.Name()))
		}
	}
	return days, nil
}

// recordFiles lists a record day's files, in record id order. The same
// filter as rawcallFiles applies: the sidecar index is skipped and so
// is anything not ending in ".json", which is what keeps a writer's
// in-flight temp out of every reader's sight.
func recordFiles(dayDir string) ([]rawcallFile, error) {
	return rawcallFiles(dayDir)
}

// readRecordIndex loads a record day's sidecar index. A missing index
// is the rebuild-from-disk case, not a failure, and an unreadable line
// costs only the metadata it carried.
func readRecordIndex(dayDir string) (map[string]recordIndexLine, error) {
	data, err := fsatomic.ReadFile(filepath.Join(dayDir, indexName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	indexed := map[string]recordIndexLine{}
	for _, raw := range bytes.Split(data, []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var line recordIndexLine
		if json.Unmarshal(raw, &line) != nil {
			continue
		}
		indexed[line.RecordID] = line
	}
	return indexed, nil
}

// recordFromIndex builds the reader's view of a file from its index
// line, or from the record's own bytes when the index missed it. read
// supplies those bytes on demand so an indexed record is never opened
// just to be described. A record that cannot be parsed still comes
// back, described only by its id and file time, so nothing on disk is
// ever invisible to a reader.
func recordFromIndex(f rawcallFile, indexed map[string]recordIndexLine, read func() ([]byte, error)) (Record, error) {
	r := Record{ID: f.id}
	if line, ok := indexed[f.id]; ok {
		r.Kind = line.RecordKind
		r.SessionID = line.SessionID
		r.ProjectIDHash = line.ProjectIDHash
		if ts, err := time.Parse(time.RFC3339Nano, line.Timestamp); err == nil {
			r.Timestamp = ts
		}
	} else {
		data, err := read()
		if err != nil {
			return Record{}, err
		}
		r = describeRecord(f.id, data)
	}
	if r.Timestamp.IsZero() {
		r.Timestamp = f.mod
	}
	return r, nil
}

// describeRecord attributes a record from its bytes alone, which is the
// rebuild path. A record that does not read back as any known kind is
// left unattributed rather than guessed at.
func describeRecord(id string, data []byte) Record {
	r := Record{ID: id}
	kind, err := envelope.KindOf(data)
	if err != nil {
		return r
	}
	var capture envelope.TranscriptCapture
	switch kind {
	case envelope.KindSegment:
		seg, err := envelope.ParseSegment(data)
		if err != nil {
			return r
		}
		r.SessionID, capture = seg.SessionID, seg.Capture
	case envelope.KindMetaSnapshot:
		snap, err := envelope.ParseMetaSnapshot(data)
		if err != nil {
			return r
		}
		r.SessionID, capture = snap.SessionID, snap.Capture
	default:
		return r
	}
	r.Kind = kind.RecordKind
	r.ProjectIDHash = capture.ProjectIDHash
	if ts, err := time.Parse(time.RFC3339Nano, capture.Timestamp); err == nil {
		r.Timestamp = ts
	}
	return r
}

// EachRecord visits every stored segment and snapshot, oldest day first
// and by record id within a day, stopping at the first error the
// visitor returns. As with rawcalls, the files are the source of truth:
// an index entry with no file is ignored and a file the index never
// mentioned is visited anyway.
func (s *Spool) EachRecord(visit func(Record) error) error {
	return s.EachRecordWhere(func(string) bool { return true }, visit)
}

// EachRecordWhere is EachRecord restricted to the records whose id
// matches. Only a matching record's bytes are read: the id is the file
// name, so selecting on it costs a directory listing rather than a read
// of every record on the machine — the same reason DeleteWhere matches
// on the id alone.
func (s *Spool) EachRecordWhere(match func(recordID string) bool, visit func(Record) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	days, err := s.recordDays()
	if err != nil {
		return err
	}
	for _, dayDir := range days {
		indexed, err := readRecordIndex(dayDir)
		if err != nil {
			return err
		}
		files, err := recordFiles(dayDir)
		if err != nil {
			return err
		}
		for _, f := range files {
			if !match(f.id) {
				continue
			}
			data, err := fsatomic.ReadFile(f.path)
			if err != nil {
				return err
			}
			r, err := recordFromIndex(f, indexed, func() ([]byte, error) { return data, nil })
			if err != nil {
				return err
			}
			r.Raw = data
			if err := visit(r); err != nil {
				return err
			}
		}
	}
	return nil
}

// OldestRecord reports when the oldest stored segment or snapshot was
// captured, consulting only the earliest day that holds one: its index
// leads and file modification times settle records the index missed.
func (s *Spool) OldestRecord() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	days, err := s.recordDays()
	if err != nil {
		return time.Time{}, false
	}
	for _, dayDir := range days {
		indexed, err := readRecordIndex(dayDir)
		if err != nil {
			return time.Time{}, false
		}
		files, err := recordFiles(dayDir)
		if err != nil {
			return time.Time{}, false
		}
		var oldest time.Time
		found := false
		for _, f := range files {
			var ts time.Time
			if line, ok := indexed[f.id]; ok {
				ts, _ = time.Parse(time.RFC3339Nano, line.Timestamp)
			}
			if ts.IsZero() {
				ts = f.mod
			}
			if !found || ts.Before(oldest) {
				oldest = ts
				found = true
			}
		}
		if found {
			return oldest, true
		}
	}
	return time.Time{}, false
}

// DeleteRecordsWhere removes every stored segment and snapshot the
// matcher accepts and rewrites each affected day index. The matcher
// sees the index's description of a record, never its bytes — Raw is
// nil — so an uploader deleting what it has shipped pays no reread. A
// record the index missed is described from its own bytes first, and
// one that cannot be parsed reaches the matcher with only its id and
// file time, so it can still be removed by id but never by attribution.
func (s *Spool) DeleteRecordsWhere(match func(Record) bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepStaleTempsLocked()
	s.rederiveLocked()
	deleted, err := s.deleteRecordsLocked(match)
	s.sig = dirSignature(s.dir)
	return deleted, err
}

// deleteRecordsLocked is the record half of every deletion. Callers
// hold s.mu and refresh the signature afterwards.
func (s *Spool) deleteRecordsLocked(match func(Record) bool) (int, error) {
	deleted := 0
	days, err := s.recordDays()
	if err != nil {
		return 0, err
	}
	for _, dayDir := range days {
		indexed, err := readRecordIndex(dayDir)
		if err != nil {
			return deleted, err
		}
		files, err := recordFiles(dayDir)
		if err != nil {
			return deleted, err
		}
		removed := map[string]bool{}
		for _, f := range files {
			r, err := recordFromIndex(f, indexed, func() ([]byte, error) { return fsatomic.ReadFile(f.path) })
			if err != nil {
				return deleted, err
			}
			if !match(r) {
				continue
			}
			if err := os.Remove(f.path); err != nil {
				return deleted, err
			}
			s.usage -= f.size
			removed[f.id] = true
			deleted++
		}
		if len(removed) > 0 {
			err := s.rewriteIndexFileLocked(filepath.Join(dayDir, indexName), func(raw []byte) bool {
				var line recordIndexLine
				return json.Unmarshal(raw, &line) == nil && removed[line.RecordID]
			})
			if err != nil {
				return deleted, err
			}
		}
	}
	return deleted, nil
}

// DeleteSession removes everything stored for one coding session on
// both sides: segments and snapshots by the session id they declare,
// rawcalls by the session id their request body carries. It reports
// the two counts separately because they answer different questions —
// how much of the session's file was waiting, and how many of its API
// calls were. Deleting is addressed by id, so an empty id is refused
// rather than matched against records that carry none.
func (s *Spool) DeleteSession(sessionID string) (rawcalls, records int, err error) {
	if sessionID == "" {
		return 0, 0, errors.New("spool: no session id to delete by")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepStaleTempsLocked()
	s.rederiveLocked()
	defer func() { s.sig = dirSignature(s.dir) }()

	rawcalls, err = s.deleteRawcallsLocked(func(f rawcallFile, indexed map[string]indexLine) (bool, error) {
		key := ""
		if line, ok := indexed[f.id]; ok && line.SessionKey != "" {
			key = line.SessionKey
		} else {
			data, err := fsatomic.ReadFile(f.path)
			if err != nil {
				return false, err
			}
			env, err := envelope.Parse(data)
			if err != nil {
				return false, nil
			}
			key = env.SessionKey()
		}
		id, ok := SessionIDFromUserID(key)
		return ok && id == sessionID, nil
	})
	if err != nil {
		return rawcalls, 0, err
	}
	records, err = s.deleteRecordsLocked(func(r Record) bool { return r.SessionID == sessionID })
	return rawcalls, records, err
}

// SessionIDFromUserID extracts the session id Claude Code embeds in
// metadata.user_id. It is used only to address records on this device
// for deletion; it is never uploaded or compared across sources.
//
// Two spellings are recognized: a JSON object carrying a session_id
// member, and the older flat string ending in "_session_<id>". Anything
// else carries no session id this package will act on.
func SessionIDFromUserID(userID string) (string, bool) {
	if strings.HasPrefix(userID, "{") {
		var v struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(userID), &v) != nil || v.SessionID == "" {
			return "", false
		}
		return v.SessionID, true
	}
	const marker = "_session_"
	i := strings.LastIndex(userID, marker)
	if i < 0 {
		return "", false
	}
	id := userID[i+len(marker):]
	return id, id != ""
}
