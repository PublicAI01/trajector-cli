package envelope

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
)

// These records carry what Claude Code itself wrote to disk: a segment
// of a session file, or a snapshot of a whole agent metadata file. They
// are not rawcalls and share no layout with them; what they share is
// the record stream, where the source and record_kind fields tell the
// two apart.
const (
	sourceTranscript = "transcript"
	kindSegment      = "segment"
	kindMetaSnapshot = "meta_snapshot"

	segmentRecordIDPrefix      = "seg_"
	metaSnapshotRecordIDPrefix = "meta_"
	recordIDHexLength          = 32
)

// Injection values report this client's own state for the project the
// session ran in: whether it forwarded the session's API traffic, or
// only read the session files.
const (
	InjectionProxy    = "proxy"
	InjectionTailOnly = "tail_only"
)

// Capture describes how a record was observed, and is the same shape
// for every record read from this machine rather than from the wire. It
// carries only what such a record does not carry itself: the consenting
// project and this client's own state at capture time.
type Capture struct {
	ClientVersion string `json:"client_version"`
	Timestamp     string `json:"timestamp"`
	ProjectIDHash string `json:"project_id_hash"`
	// ProjectSubpath is absent, never empty, when the session started at
	// the project root.
	ProjectSubpath string `json:"project_subpath,omitempty"`
	Injection      string `json:"injection"`
}

// Segment is one run of complete lines from one session file. Lines
// holds the redacted lines as one byte string with their newlines kept;
// it is never split into an array, and every line in it ends in a
// newline. Every JSON tag is part of the documented contract, and the
// field order is the serialized order.
type Segment struct {
	SchemaVersion string `json:"schema_version"`
	Source        string `json:"source"`
	RecordKind    string `json:"record_kind"`
	RecordID      string `json:"record_id"`
	SessionID     string `json:"session_id"`
	// File is empty for the session's main file and names the file
	// relative to the session directory otherwise.
	File string `json:"file"`
	// SegmentIndex counts up from 0 per (session, file) and is never
	// reused: a rewritten file is read again from the start and its new
	// segments take new indexes.
	SegmentIndex int     `json:"segment_index"`
	Capture      Capture `json:"capture"`
	Lines        string  `json:"lines"`
}

// MetaSnapshot is one whole agent metadata file at one moment. The file
// is overwritten in place rather than appended to, so a snapshot has no
// index: a later snapshot of the same file replaces an earlier one, and
// its record id changes with its content.
type MetaSnapshot struct {
	SchemaVersion string  `json:"schema_version"`
	Source        string  `json:"source"`
	RecordKind    string  `json:"record_kind"`
	RecordID      string  `json:"record_id"`
	SessionID     string  `json:"session_id"`
	File          string  `json:"file"`
	Capture       Capture `json:"capture"`
	// Content is the redacted file kept verbatim: its bytes are stored
	// as they were read, never re-serialized.
	Content json.RawMessage `json:"content"`
}

// NewSegment builds a segment record and names it from its identity.
func NewSegment(sessionID, file string, index int, capture Capture, lines string) Segment {
	return Segment{
		SchemaVersion: SchemaVersion,
		Source:        sourceTranscript,
		RecordKind:    kindSegment,
		RecordID:      SegmentRecordID(sessionID, file, index),
		SessionID:     sessionID,
		File:          file,
		SegmentIndex:  index,
		Capture:       capture,
		Lines:         lines,
	}
}

// NewMetaSnapshot builds a snapshot record and names it from its
// identity and content. It fails only when content is not a JSON
// document, because such content has no canonical form to name.
func NewMetaSnapshot(sessionID, file string, capture Capture, content []byte) (MetaSnapshot, error) {
	id, err := MetaSnapshotRecordID(sessionID, file, content)
	if err != nil {
		return MetaSnapshot{}, err
	}
	return MetaSnapshot{
		SchemaVersion: SchemaVersion,
		Source:        sourceTranscript,
		RecordKind:    kindMetaSnapshot,
		RecordID:      id,
		SessionID:     sessionID,
		File:          file,
		Capture:       capture,
		Content:       json.RawMessage(append([]byte(nil), content...)),
	}, nil
}

// SegmentRecordID names a segment from its identity alone, so the same
// segment sent again after a crash carries the same id.
func SegmentRecordID(sessionID, file string, index int) string {
	h := sha256.New()
	h.Write([]byte(sessionID))
	h.Write([]byte{0})
	h.Write([]byte(file))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(index)))
	return segmentRecordIDPrefix + hex.EncodeToString(h.Sum(nil))[:recordIDHexLength]
}

// MetaSnapshotRecordID names a snapshot from its identity and the
// canonical form of its content, so unchanged content sent again
// carries the same id and changed content carries a new one. The
// content digest enters the hash as its lowercase hex text.
func MetaSnapshotRecordID(sessionID, file string, content []byte) (string, error) {
	canonical, err := canonicalJSON(content)
	if err != nil {
		return "", fmt.Errorf("envelope: naming snapshot: %w", err)
	}
	contentDigest := sha256.Sum256(canonical)
	h := sha256.New()
	h.Write([]byte(sessionID))
	h.Write([]byte{0})
	h.Write([]byte(file))
	h.Write([]byte{0})
	h.Write([]byte(hex.EncodeToString(contentDigest[:])))
	return metaSnapshotRecordIDPrefix + hex.EncodeToString(h.Sum(nil))[:recordIDHexLength], nil
}

// canonicalJSON is the one serialization every client must agree on:
// object keys in byte order, no whitespace, no HTML escaping, and
// numbers kept as written.
func canonicalJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON document")
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// Restated returns the segment with its schema version set to the
// current one, for the same reason a rawcall is restated: a batch and
// the records in it declare one version.
func (s Segment) Restated() Segment {
	s.SchemaVersion = SchemaVersion
	return s
}

// Restated returns the snapshot with its schema version set to the
// current one.
func (m MetaSnapshot) Restated() MetaSnapshot {
	m.SchemaVersion = SchemaVersion
	return m
}

// Bytes serializes the segment.
func (s Segment) Bytes() ([]byte, error) { return marshalRecord(s) }

// Bytes serializes the snapshot.
func (m MetaSnapshot) Bytes() ([]byte, error) { return marshalRecord(m) }

// marshalRecord keeps the stored bytes exactly what the fields say: no
// HTML escaping, no trailing newline.
func marshalRecord(v any) ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("envelope: serializing transcript record: %w", err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// ParseSegment reads a stored segment back, refusing any record that
// does not declare itself as one.
func ParseSegment(data []byte) (Segment, error) {
	var s Segment
	if err := json.Unmarshal(data, &s); err != nil {
		return Segment{}, fmt.Errorf("envelope: reading segment: %w", err)
	}
	if err := checkTranscriptHeader(s.SchemaVersion, s.Source, s.RecordKind, kindSegment); err != nil {
		return Segment{}, err
	}
	return s, nil
}

// ParseMetaSnapshot reads a stored snapshot back, refusing any record
// that does not declare itself as one.
func ParseMetaSnapshot(data []byte) (MetaSnapshot, error) {
	var m MetaSnapshot
	if err := json.Unmarshal(data, &m); err != nil {
		return MetaSnapshot{}, fmt.Errorf("envelope: reading snapshot: %w", err)
	}
	if err := checkTranscriptHeader(m.SchemaVersion, m.Source, m.RecordKind, kindMetaSnapshot); err != nil {
		return MetaSnapshot{}, err
	}
	return m, nil
}

func checkTranscriptHeader(version, source, kind, wantKind string) error {
	if err := checkVersion(version); err != nil {
		return err
	}
	if source != sourceTranscript || kind != wantKind {
		return fmt.Errorf("envelope: record is %s/%s, not %s/%s", source, kind, sourceTranscript, wantKind)
	}
	return nil
}

// Kind is what a stored record declares itself to be, read before
// anything else about it is interpreted. RecordKind is empty for a
// rawcall.
type Kind struct {
	Source     string
	RecordKind string
}

// Kind values of the records this package can read.
var (
	KindRawcall      = Kind{Source: sourceProxy}
	KindSegment      = Kind{Source: sourceTranscript, RecordKind: kindSegment}
	KindMetaSnapshot = Kind{Source: sourceTranscript, RecordKind: kindMetaSnapshot}
	KindGitSnapshot  = Kind{Source: sourceHook, RecordKind: kindGitSnapshot}
)

// KindOf reads only a record's self-declaration, so a caller can pick
// the parser before committing to a layout.
func KindOf(data []byte) (Kind, error) {
	var k struct {
		Source     string `json:"source"`
		RecordKind string `json:"record_kind"`
	}
	if err := json.Unmarshal(data, &k); err != nil {
		return Kind{}, fmt.Errorf("envelope: reading record kind: %w", err)
	}
	return Kind{Source: k.Source, RecordKind: k.RecordKind}, nil
}
