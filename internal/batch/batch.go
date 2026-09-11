// Package batch prepares spooled records for one upload. A batch is
// two parts: an uncompressed envelope naming the batch and indexing
// every record it carries, and a zstd-compressed stream of the records
// themselves, masked by redaction and laid out so records of the same
// session sit adjacent. The envelope stays uncompressed so a receiver
// can route a batch without unpacking it; its serialized layout is a
// product contract like the record envelopes'.
//
// Records of every kind ride in one batch: rawcalls, and the segments
// and snapshots the spool's second slot holds. They share the stream
// and the index, and nothing else — each slot is masked by its own
// redaction pass and ordered by its own session key.
package batch

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/redact"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// Run is the capture runtime metadata one batch carries alongside its
// records. All values are counters and gauges observed on this machine;
// they ride along so no separate telemetry channel is needed.
type Run struct {
	RecordedToday    int   `json:"recorded_today"`
	SSEDegradedToday int   `json:"sse_degraded_today"`
	CapturesDropped  int   `json:"captures_dropped"`
	SpoolUsageBytes  int64 `json:"spool_usage_bytes"`
	SpoolQuotaBytes  int64 `json:"spool_quota_bytes"`
}

// Contents is a set of records addressed by the slot each is stored
// in: the rawcalls, and the segments and snapshots read from session
// files. It is what a batch is built from and what a batch reports it
// packed, so a caller deleting or setting aside what a batch carried
// addresses each slot exactly.
type Contents struct {
	Rawcalls       []spool.Rawcall
	SessionRecords []spool.Record
}

// Len counts the records across both slots.
func (c Contents) Len() int { return len(c.Rawcalls) + len(c.SessionRecords) }

// IDs lists every record's spool id, rawcalls first.
func (c Contents) IDs() []string {
	ids := make([]string, 0, c.Len())
	for _, rc := range c.Rawcalls {
		ids = append(ids, rc.RequestID)
	}
	for _, r := range c.SessionRecords {
		ids = append(ids, r.ID)
	}
	return ids
}

// Batch is one upload payload, ready for the wire.
type Batch struct {
	// ID is the batch's idempotency key: a receiver seeing the same ID
	// twice must ingest the batch once.
	ID string
	// Envelope is the uncompressed identity, index, and run metadata.
	Envelope []byte
	// Records is the zstd-compressed stream of redacted records. The
	// type carries the proof from the redaction pass to the network exit:
	// nothing else can flow into an upload.
	Records redact.RedactedBytes
	// Packed is every spool entry this batch carries, in stream order,
	// so an acknowledged upload deletes exactly what was sent. Entries
	// are named by their spool ids: a snapshot whose id changed under
	// redaction is indexed in the envelope by the new id and named here
	// by the id its file has.
	Packed Contents
}

// Refusal is one entry Build set aside instead of packing: a record
// that no longer reads back as what it claims to be, or that redaction
// could not mask. Such a record cannot be attributed or masked
// field-aware, so it must not ship. Refusal is never silent loss: the
// entry rides out whole so the caller decides where it waits. Exactly
// one of Rawcall and Record is set, by the slot the entry came from.
type Refusal struct {
	Rawcall spool.Rawcall
	Record  spool.Record
	Err     error
}

// ID is the refused entry's spool id.
func (r Refusal) ID() string {
	if r.Record.ID != "" {
		return r.Record.ID
	}
	return r.Rawcall.RequestID
}

// Build packs spool entries into one batch. Every entry passes through
// redaction before it is packed — nothing leaves this function unmasked
// — and the stream is laid out for compression: rawcalls first, then
// segments and snapshots, each kind grouped by its own session so
// records whose bodies share long prefixes sit adjacent.
//
// One pass answers for every input entry: it is either packed into the
// batch or returned among the refusals, so one bad record never stops
// or slows the rest and the caller learns of every refusal at once. The
// error reports what failed the build itself; a zero batch with a nil
// error means no packable entry was left.
func Build(id string, createdAt time.Time, clientVersion string, in Contents, run Run) (Batch, []Refusal, error) {
	if id == "" {
		return Batch{}, nil, fmt.Errorf("batch: a batch needs an id")
	}
	if in.Len() == 0 {
		return Batch{}, nil, fmt.Errorf("batch: a batch needs at least one record")
	}

	var (
		stream  bytes.Buffer
		refused []Refusal
		packed  Contents
	)
	ix := newIndexV2(id, createdAt, clientVersion, run)
	pack := func(item IndexItemV2, masked redact.RedactedBytes) {
		ix.add(item, int64(masked.Len()))
		stream.Write(masked.Bytes())
	}

	for _, rc := range orderRawcalls(in.Rawcalls) {
		item, masked, err := packRawcall(rc)
		if err != nil {
			refused = append(refused, Refusal{Rawcall: rc, Err: err})
			continue
		}
		pack(item, masked)
		packed.Rawcalls = append(packed.Rawcalls, rc)
	}

	ordered, unreadable := orderRecords(in.SessionRecords)
	refused = append(refused, unreadable...)
	for _, r := range ordered {
		item, masked, err := r.pack()
		if err != nil {
			refused = append(refused, Refusal{Record: r.stored, Err: err})
			continue
		}
		pack(item, masked)
		packed.SessionRecords = append(packed.SessionRecords, r.stored)
	}

	if packed.Len() == 0 {
		return Batch{}, refused, nil
	}

	// The stream is assembled exclusively from RedactedBytes above, so
	// its compressed form is still redacted data.
	compressed, err := compress(stream.Bytes())
	if err != nil {
		return Batch{}, refused, fmt.Errorf("batch: compressing records: %w", err)
	}
	env, err := ix.Bytes()
	if err != nil {
		return Batch{}, refused, err
	}
	return Batch{ID: id, Envelope: env, Records: redact.AlreadyRedacted(compressed), Packed: packed}, refused, nil
}

// orderRawcalls lays rawcalls out by (session key, timestamp, id).
func orderRawcalls(rawcalls []spool.Rawcall) []spool.Rawcall {
	ordered := append([]spool.Rawcall(nil), rawcalls...)
	for i := range ordered {
		// The day index is advisory: a record it missed still carries its
		// session identity in its own envelope, and adjacency must not
		// degrade just because the index was lost.
		if ordered[i].SessionKey == "" {
			if env, err := envelope.Parse(ordered[i].Data); err == nil {
				ordered[i].SessionKey = env.SessionKey()
			}
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		// Records without a session identity sort after every session
		// group rather than splitting them apart.
		if (a.SessionKey == "") != (b.SessionKey == "") {
			return b.SessionKey == ""
		}
		if a.SessionKey != b.SessionKey {
			return a.SessionKey < b.SessionKey
		}
		if !a.Timestamp.Equal(b.Timestamp) {
			return a.Timestamp.Before(b.Timestamp)
		}
		return a.RequestID < b.RequestID
	})
	return ordered
}

// packRawcall masks one rawcall and indexes it. The index copies what
// the rawcall's own envelope says, so a record whose envelope cannot be
// read is refused before redaction is even attempted.
func packRawcall(rc spool.Rawcall) (IndexItemV2, redact.RedactedBytes, error) {
	env, err := envelope.Parse(rc.Data)
	if err != nil {
		return IndexItemV2{}, redact.RedactedBytes{}, err
	}
	masked, err := redact.JSONLBytes(rc.Data)
	if err != nil {
		// An unmaskable record must not be shipped.
		return IndexItemV2{}, redact.RedactedBytes{}, fmt.Errorf("redacting: %w", err)
	}
	item := rawcallItem(env)
	if item.Timestamp == "" && !rc.Timestamp.IsZero() {
		item.Timestamp = rc.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	return item, masked, nil
}

// snapshotOrder is the segment index a snapshot sorts at: below every
// segment of the same file, so the order over both kinds is total.
const snapshotOrder = -1

// parsedRecord is one segment or snapshot read back from its bytes,
// with the fields its order and its packing depend on.
type parsedRecord struct {
	stored   spool.Record
	kind     envelope.Kind
	segment  envelope.Segment
	snapshot envelope.MetaSnapshot
	// The sort key: session, then file, then position in the file.
	sessionID string
	file      string
	index     int
	timestamp string
	recordID  string
}

// orderRecords reads every record back and lays the readable ones out
// by (session id, file, segment index, timestamp, id). Segments are
// ordered by their index and never by their id: the id is a digest and
// would scatter a file's segments. A record that does not read back as
// a segment or a snapshot has no place in that order and is returned
// as a refusal.
func orderRecords(records []spool.Record) ([]parsedRecord, []Refusal) {
	var (
		ordered []parsedRecord
		refused []Refusal
	)
	for _, r := range records {
		p, err := parseRecord(r)
		if err != nil {
			refused = append(refused, Refusal{Record: r, Err: err})
			continue
		}
		ordered = append(ordered, p)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.sessionID != b.sessionID {
			return a.sessionID < b.sessionID
		}
		if a.file != b.file {
			return a.file < b.file
		}
		if a.index != b.index {
			return a.index < b.index
		}
		if a.timestamp != b.timestamp {
			return a.timestamp < b.timestamp
		}
		return a.recordID < b.recordID
	})
	return ordered, refused
}

// parseRecord reads a record by what its bytes declare, never by what
// the spool index says about it: the file is the source of truth.
func parseRecord(r spool.Record) (parsedRecord, error) {
	kind, err := envelope.KindOf(r.Raw)
	if err != nil {
		return parsedRecord{}, err
	}
	p := parsedRecord{stored: r, kind: kind}
	switch kind {
	case envelope.KindSegment:
		seg, err := envelope.ParseSegment(r.Raw)
		if err != nil {
			return parsedRecord{}, err
		}
		p.segment = seg
		p.sessionID, p.file, p.index = seg.SessionID, seg.File, seg.SegmentIndex
		p.timestamp, p.recordID = seg.Capture.Timestamp, seg.RecordID
	case envelope.KindMetaSnapshot:
		snap, err := envelope.ParseMetaSnapshot(r.Raw)
		if err != nil {
			return parsedRecord{}, err
		}
		p.snapshot = snap
		p.sessionID, p.file, p.index = snap.SessionID, snap.File, snapshotOrder
		p.timestamp, p.recordID = snap.Capture.Timestamp, snap.RecordID
	default:
		return parsedRecord{}, fmt.Errorf("record %s declares %s/%s, which is not a record kind this batch can carry", r.ID, kind.Source, kind.RecordKind)
	}
	return p, nil
}

// pack masks one record and indexes it. A segment is masked as a
// whole and then serialized: the lines are the unit redaction knows,
// and a second pass over the serialized record would see them as one
// string and undo the field policy that kept signatures intact. A
// snapshot is masked the same way, and is indexed by the id the masked
// content names, since the id must name what the record carries.
func (p parsedRecord) pack() (IndexItemV2, redact.RedactedBytes, error) {
	if p.kind == envelope.KindSegment {
		seg, err := redact.RedactSegment(p.segment)
		if err != nil {
			return IndexItemV2{}, redact.RedactedBytes{}, fmt.Errorf("redacting: %w", err)
		}
		data, err := seg.Bytes()
		if err != nil {
			return IndexItemV2{}, redact.RedactedBytes{}, err
		}
		return segmentItem(seg), redact.AlreadyRedacted(data), nil
	}
	snap, err := redact.RedactMetaSnapshot(p.snapshot)
	if err != nil {
		return IndexItemV2{}, redact.RedactedBytes{}, fmt.Errorf("redacting: %w", err)
	}
	data, err := snap.Bytes()
	if err != nil {
		return IndexItemV2{}, redact.RedactedBytes{}, err
	}
	return metaSnapshotItem(snap), redact.AlreadyRedacted(data), nil
}

func compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(data); err != nil {
		zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
