// Package batch prepares spooled records for one upload. A batch is
// two parts: an uncompressed envelope naming the batch and indexing
// every record it carries, and a zstd-compressed stream of the records
// themselves, masked by redaction and laid out so records of the same
// session sit adjacent. The envelope stays uncompressed so a receiver
// can route a batch without unpacking it; its serialized layout is a
// product contract like the record envelopes'.
//
// Records of every kind ride in one batch: rawcalls, and everything the
// spool's second slot holds. They share the stream and the index, and
// nothing else — each record is masked by the pass its own kind needs
// and ordered by its own session key.
package batch

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/mediablock"
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
	Packed spool.Entries
	// Originals names every image or document payload the records carry
	// in full, by scope and digest. Once the service acknowledges the
	// batch, it holds each of them, and a later batch may refer to them.
	Originals []mediablock.Original
}

// Refusal is one entry Build set aside instead of packing: a record
// that no longer reads back as what it claims to be, or that redaction
// could not mask. Such a record cannot be attributed or masked
// field-aware, so it must not ship. Refusal is never silent loss: the
// entry rides out whole, so the caller decides where it waits.
type Refusal struct {
	Entry spool.Entry
	Err   error
}

// ID is the refused entry's spool id.
func (r Refusal) ID() string { return r.Entry.ID }

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
//
// Before a record is masked, the image and document content it carries
// is rewritten under blocks, by the one pass every kind of record goes
// through, and redaction then masks what the rewrite left. The zero
// policy sends the content as it is.
func Build(id string, createdAt time.Time, clientVersion string, in spool.Entries, run Run, blocks mediablock.Policy) (Batch, []Refusal, error) {
	if id == "" {
		return Batch{}, nil, fmt.Errorf("batch: a batch needs an id")
	}
	if len(in) == 0 {
		return Batch{}, nil, fmt.Errorf("batch: a batch needs at least one record")
	}

	var (
		ready   []packable
		refused []Refusal
	)
	for _, e := range in {
		p, err := read(e)
		if err != nil {
			refused = append(refused, Refusal{Entry: e, Err: err})
			continue
		}
		ready = append(ready, p)
	}
	sort.SliceStable(ready, func(i, j int) bool { return ready[i].at.before(ready[j].at) })

	var (
		stream bytes.Buffer
		packed spool.Entries
	)
	ix := newIndex(id, createdAt, clientVersion, run)
	pass := mediablock.NewPass(blocks)
	for _, p := range ready {
		var rewritten mediablock.Rewritten
		rewrite := func(masked []byte) []byte {
			rewritten = pass.Rewrite(p.scope, masked)
			return rewritten.Data
		}
		item, masked, err := p.mask(rewrite)
		if err != nil {
			refused = append(refused, Refusal{Entry: p.entry, Err: err})
			continue
		}
		pass.Keep(rewritten)
		ix.add(item, int64(masked.Len()))
		stream.Write(masked.Bytes())
		packed = append(packed, p.entry)
	}

	if len(packed) == 0 {
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
	return Batch{ID: id, Envelope: env, Records: redact.AlreadyRedacted(compressed), Packed: packed, Originals: pass.Originals()}, refused, nil
}

// The slots, in the order their records ride in the stream.
const (
	rawcallSlot = iota
	sessionRecordSlot
	hookRecordSlot
)

// snapshotOrder is the segment index a snapshot sorts at: below every
// segment of the same file, so the order over both kinds is total.
const snapshotOrder = -1

// placement is where one record sits in the stream: beside the records
// of its own session, so bodies that share long prefixes compress
// together. The slot decides first, so the records of the two slots
// never interleave, and each kind fills only the fields its own order
// depends on.
type placement struct {
	slot    int
	session string
	// unnamed sorts a record that names no session after every session
	// group, rather than splitting the groups apart.
	unnamed bool
	file    string
	index   int
	when    time.Time
	id      string
}

func (a placement) before(b placement) bool {
	switch {
	case a.slot != b.slot:
		return a.slot < b.slot
	case a.unnamed != b.unnamed:
		return b.unnamed
	case a.session != b.session:
		return a.session < b.session
	case a.file != b.file:
		return a.file < b.file
	case a.index != b.index:
		return a.index < b.index
	case !a.when.Equal(b.when):
		return a.when.Before(b.when)
	}
	return a.id < b.id
}

// packable is one entry read back from its own bytes, with where it
// sits in the stream and how it is masked and indexed. The kind is
// decided once, when the entry is read, so every pass after that is
// the same pass for every kind.
//
// scope is the scope the record's image and document blocks are
// counted in, empty for a record that has none. mask applies rewrite
// to the bytes that hold those blocks, before it masks them, and to
// nothing else.
type packable struct {
	entry spool.Entry
	at    placement
	scope string
	mask  func(rewrite func([]byte) []byte) (IndexItem, redact.RedactedBytes, error)
}

// A scope names the session a record's blocks are counted in. The
// recording path is part of the name: a rawcall names its session one
// way and a session file another, the two are never converted into
// each other, and a reference is only ever to a payload that the same
// path carried. A record with no scope never refers to a payload, and
// no other record refers to its payloads.
//
// A reference must resolve in the session the service places the
// record in, so a scope never spans two of the service's sessions. It
// can be smaller than one: that costs one more original of a payload,
// never a reference that does not resolve.

// rawcallScope is the scope of a rawcall: the session identity the
// request states, the project, and the UTC date the record states. The
// service places the rawcalls of one session identity in one session
// when it can read a session from that identity, and otherwise in one
// session for each project and UTC date. A scope that names all three
// is inside the service's session in both cases. A session that runs
// past midnight UTC, or across projects, sends each image in full once
// more for each new date or project.
//
// A rawcall that states no session identity is a session of its own on
// the service side, so it has no scope.
func rawcallScope(session, projectIDHash, timestamp string) string {
	if session == "" {
		return ""
	}
	return scopeName(envelope.KindRawcall.Source, session, projectIDHash, statedDate(timestamp))
}

// segmentScope is the scope of a segment: the session the segment
// states, which is the session the service places it in.
func segmentScope(session string) string {
	if session == "" {
		return ""
	}
	return scopeName(envelope.KindSegment.Source, session)
}

// scopeName joins the parts of a scope. Each part is quoted, so two
// different lists of parts never give the same name, whatever bytes a
// session identity holds.
func scopeName(parts ...string) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(strconv.Quote(part))
	}
	return b.String()
}

// statedDate is the date part of a timestamp as the record states it:
// its first ten characters, which is how the service reads the date.
// Every record states its timestamp in UTC as RFC 3339, so this is the
// UTC date, YYYY-MM-DD. A timestamp that is not all ASCII is taken
// whole: the service can count its characters in a different unit, and
// the whole text never puts two records in one scope when their dates
// differ.
func statedDate(timestamp string) string {
	const dateLen = len("2006-01-02")
	for i := range len(timestamp) {
		if timestamp[i] >= utf8.RuneSelf {
			return timestamp
		}
	}
	return timestamp[:min(dateLen, len(timestamp))]
}

// read reads one entry by what its bytes declare, never by what the
// spool index says about it: the file is the source of truth. A record
// that declares a kind no batch can carry, or that does not read back
// as the kind it declares, has no place in the order and is refused
// here, before redaction is even attempted.
func read(e spool.Entry) (packable, error) {
	kind, err := envelope.KindOf(e.Raw)
	if err != nil {
		return packable{}, err
	}
	switch kind {
	case envelope.KindRawcall:
		env, err := envelope.Parse(e.Raw)
		if err != nil {
			return packable{}, err
		}
		// The session identity is read from the record, never from the
		// day index: the index is advisory and can be lost or disagree
		// with the record, and the scope built from this identity must
		// stay inside the session the service places the record in. The
		// cost is one more decoding of the request body per rawcall.
		session := env.SessionKey()
		return packable{
			entry: e,
			at:    placement{slot: rawcallSlot, session: session, unnamed: session == "", when: e.Timestamp, id: e.ID},
			scope: rawcallScope(session, env.ProjectIDHash(), env.StatedTimestamp()),
			mask: func(rewrite func([]byte) []byte) (IndexItem, redact.RedactedBytes, error) {
				return maskRawcall(kind, e, env, rewrite)
			},
		}, nil
	case envelope.KindSegment:
		seg, err := envelope.ParseSegment(e.Raw)
		if err != nil {
			return packable{}, err
		}
		// Segments are ordered by their index and never by their id: the
		// id is a digest and would scatter a file's segments.
		return packable{
			entry: e,
			at: placement{
				slot: sessionRecordSlot, session: seg.SessionID, unnamed: seg.SessionID == "",
				file: seg.File, index: seg.SegmentIndex, when: captureTime(seg.Capture), id: seg.RecordID,
			},
			scope: segmentScope(seg.SessionID),
			mask: func(rewrite func([]byte) []byte) (IndexItem, redact.RedactedBytes, error) {
				return maskSegment(kind, seg, rewrite)
			},
		}, nil
	case envelope.KindMetaSnapshot:
		snap, err := envelope.ParseMetaSnapshot(e.Raw)
		if err != nil {
			return packable{}, err
		}
		return packable{
			entry: e,
			at: placement{
				slot: sessionRecordSlot, session: snap.SessionID, unnamed: snap.SessionID == "",
				file: snap.File, index: snapshotOrder, when: captureTime(snap.Capture), id: snap.RecordID,
			},
			mask: func(func([]byte) []byte) (IndexItem, redact.RedactedBytes, error) {
				return maskMetaSnapshot(kind, snap)
			},
		}, nil
	case envelope.KindGitSnapshot:
		snap, err := envelope.ParseGitSnapshot(e.Raw)
		if err != nil {
			return packable{}, err
		}
		// A git snapshot names no file and has no index of its own, so
		// its order inside its session is the order it was observed in.
		return packable{
			entry: e,
			at: placement{
				slot: hookRecordSlot, session: snap.SessionID, unnamed: snap.SessionID == "",
				when: captureTime(snap.Capture), id: snap.RecordID,
			},
			mask: func(func([]byte) []byte) (IndexItem, redact.RedactedBytes, error) {
				return maskGitSnapshot(kind, snap)
			},
		}, nil
	}
	return packable{}, fmt.Errorf("record %s declares %s/%s, which is not a record kind this batch can carry", e.ID, kind.Source, kind.RecordKind)
}

// captureTime is when a record read from a session file was captured.
// A timestamp that does not read back leaves the record at the front of
// its own file's group rather than out of the order altogether.
func captureTime(capture envelope.Capture) time.Time {
	at, _ := time.Parse(time.RFC3339Nano, capture.Timestamp)
	return at
}

// maskRawcall rewrites one rawcall's blocks, masks it, and indexes it.
// The index copies what the rawcall's own envelope says.
func maskRawcall(kind envelope.Kind, e spool.Entry, env envelope.Envelope, rewrite func([]byte) []byte) (IndexItem, redact.RedactedBytes, error) {
	// The batch and every record in it declare one version, so a record
	// stored under an earlier one is restated before it is masked.
	stored, err := env.Restated()
	if err != nil {
		return IndexItem{}, redact.RedactedBytes{}, err
	}
	masked, err := redact.JSONLBytes(rewrite(stored))
	if err != nil {
		// An unmaskable record must not be shipped.
		return IndexItem{}, redact.RedactedBytes{}, fmt.Errorf("redacting: %w", err)
	}
	item := rawcallItem(kind, env)
	if item.Timestamp == "" && !e.Timestamp.IsZero() {
		item.Timestamp = e.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	return item, masked, nil
}

// maskSegment rewrites the blocks in one segment's lines, masks the
// segment as a whole, and then serializes it: the lines are the unit
// redaction knows, and a second pass over the serialized record would
// see them as one string and undo the field policy that kept signatures
// intact. The blocks are rewritten in the lines for the same reason: in
// the serialized record they are inside one string and no longer
// objects.
//
// The rewrite comes first because redaction reads a Read result's copy
// of the content as text and masks parts of it: a placeholder taken
// after that would state a digest and a size of no content at all.
func maskSegment(kind envelope.Kind, seg envelope.Segment, rewrite func([]byte) []byte) (IndexItem, redact.RedactedBytes, error) {
	seg.Lines = string(rewrite([]byte(seg.Lines)))
	masked, err := redact.RedactSegment(seg)
	if err != nil {
		return IndexItem{}, redact.RedactedBytes{}, fmt.Errorf("redacting: %w", err)
	}
	masked = masked.Restated()
	data, err := masked.Bytes()
	if err != nil {
		return IndexItem{}, redact.RedactedBytes{}, err
	}
	return sessionRecordItem(kind, masked.RecordID, masked.Capture), redact.AlreadyRedacted(data), nil
}

// maskMetaSnapshot masks one snapshot the same way, and indexes it by
// the id the masked content names, since the id must name what the
// record carries.
func maskMetaSnapshot(kind envelope.Kind, snap envelope.MetaSnapshot) (IndexItem, redact.RedactedBytes, error) {
	masked, err := redact.RedactMetaSnapshot(snap)
	if err != nil {
		return IndexItem{}, redact.RedactedBytes{}, fmt.Errorf("redacting: %w", err)
	}
	masked = masked.Restated()
	data, err := masked.Bytes()
	if err != nil {
		return IndexItem{}, redact.RedactedBytes{}, err
	}
	return sessionRecordItem(kind, masked.RecordID, masked.Capture), redact.AlreadyRedacted(data), nil
}

// maskGitSnapshot masks one git snapshot and indexes it. Only the
// observed paths are scanned: every other field the record carries is a
// commit or blob identifier this client read from git, and a scan could
// only damage those.
func maskGitSnapshot(kind envelope.Kind, snap envelope.GitSnapshot) (IndexItem, redact.RedactedBytes, error) {
	masked, err := redact.RedactGitSnapshot(snap)
	if err != nil {
		return IndexItem{}, redact.RedactedBytes{}, fmt.Errorf("redacting: %w", err)
	}
	masked = masked.Restated()
	data, err := masked.Bytes()
	if err != nil {
		return IndexItem{}, redact.RedactedBytes{}, err
	}
	return sessionRecordItem(kind, masked.RecordID, masked.Capture), redact.AlreadyRedacted(data), nil
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
