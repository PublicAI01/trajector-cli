// Package batch prepares captured rawcalls for one upload. A batch is
// two parts: an uncompressed envelope naming the batch and indexing
// every record it carries, and a zstd-compressed stream of the records
// themselves, masked by redaction and laid out so records of the same
// session sit adjacent. The envelope stays uncompressed so a receiver
// can route a batch without unpacking it; its serialized layout is a
// product contract like the rawcall envelope's.
package batch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/redact"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

const schemaVersion = "1"

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

// wire is the serialized batch envelope. Every JSON tag is part of the
// documented contract.
type wire struct {
	SchemaVersion string     `json:"schema_version"`
	BatchID       string     `json:"batch_id"`
	ClientVersion string     `json:"client_version"`
	CreatedAt     string     `json:"created_at"`
	Compression   string     `json:"compression"`
	RecordsSize   int64      `json:"records_size"`
	Records       []wireItem `json:"records"`
	Run           Run        `json:"run"`
}

// wireItem indexes one record inside the decompressed stream. The
// metadata fields are copies of what the rawcall's own envelope says;
// every packed record has a readable envelope, because Build refuses
// the ones that do not.
type wireItem struct {
	RequestID      string `json:"request_id"`
	ProjectIDHash  string `json:"project_id_hash,omitempty"`
	UpstreamOrigin string `json:"upstream_origin,omitempty"`
	Endpoint       string `json:"endpoint,omitempty"`
	Timestamp      string `json:"timestamp,omitempty"`
	Garbled        bool   `json:"garbled,omitempty"`
	Offset         int64  `json:"offset"`
	Size           int64  `json:"size"`
}

// Batch is one upload payload, ready for the wire.
type Batch struct {
	// ID is the batch's idempotency key: a receiver seeing the same ID
	// twice must ingest the batch once.
	ID string
	// Envelope is the uncompressed identity, index, and run metadata.
	Envelope []byte
	// Records is the zstd-compressed stream of redacted rawcalls. The
	// type carries the proof from the redaction pass to the network exit:
	// nothing else can flow into an upload.
	Records redact.RedactedBytes
	// RequestIDs names every spool record packed into this batch, so an
	// acknowledged upload can delete exactly what was sent.
	RequestIDs []string
}

// Refusal is one rawcall Build set aside instead of packing: a record
// that no longer reads back as a rawcall, or that redaction could not
// mask. Such a record cannot be attributed or masked field-aware, so it
// must not ship. Refusal is never silent loss: the record rides out
// whole so the caller decides where it waits.
type Refusal struct {
	Rawcall spool.Rawcall
	Err     error
}

// Build packs rawcalls into one batch. Records of the same session are
// laid out adjacently — their bodies share long prefixes, which is what
// makes the compressed stream small — and every record passes through
// redaction before it is packed: nothing leaves this function unmasked.
//
// One pass answers for every input record: it is either packed into the
// batch or returned among the refusals, so one bad record never stops
// or slows the rest and the caller learns of every refusal at once. The
// error reports what failed the build itself; a zero batch with a nil
// error means no packable record was left.
func Build(id string, createdAt time.Time, clientVersion string, rawcalls []spool.Rawcall, run Run) (Batch, []Refusal, error) {
	if id == "" {
		return Batch{}, nil, fmt.Errorf("batch: a batch needs an id")
	}
	if len(rawcalls) == 0 {
		return Batch{}, nil, fmt.Errorf("batch: a batch needs at least one rawcall")
	}

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

	var stream bytes.Buffer
	var refused []Refusal
	items := make([]wireItem, 0, len(ordered))
	ids := make([]string, 0, len(ordered))
	for _, rc := range ordered {
		env, err := envelope.Parse(rc.Data)
		if err != nil {
			refused = append(refused, Refusal{Rawcall: rc, Err: err})
			continue
		}
		masked, err := redact.JSONLBytes(rc.Data)
		if err != nil {
			// An unmaskable record must not be shipped.
			refused = append(refused, Refusal{Rawcall: rc, Err: fmt.Errorf("redacting: %w", err)})
			continue
		}
		item := index(rc, env)
		item.Offset = int64(stream.Len())
		item.Size = int64(masked.Len())
		stream.Write(masked.Bytes())
		items = append(items, item)
		ids = append(ids, rc.RequestID)
	}
	if len(items) == 0 {
		return Batch{}, refused, nil
	}

	// The stream is assembled exclusively from RedactedBytes above, so
	// its compressed form is still redacted data.
	compressed, err := compress(stream.Bytes())
	if err != nil {
		return Batch{}, refused, fmt.Errorf("batch: compressing records: %w", err)
	}

	env, err := json.Marshal(wire{
		SchemaVersion: schemaVersion,
		BatchID:       id,
		ClientVersion: clientVersion,
		CreatedAt:     createdAt.UTC().Format(time.RFC3339Nano),
		Compression:   "zstd",
		RecordsSize:   int64(stream.Len()),
		Records:       items,
		Run:           run,
	})
	if err != nil {
		return Batch{}, refused, fmt.Errorf("batch: serializing envelope: %w", err)
	}
	return Batch{ID: id, Envelope: env, Records: redact.AlreadyRedacted(compressed), RequestIDs: ids}, refused, nil
}

// index copies what a rawcall's envelope says about it.
func index(rc spool.Rawcall, env envelope.Envelope) wireItem {
	item := wireItem{RequestID: rc.RequestID}
	if !rc.Timestamp.IsZero() {
		item.Timestamp = rc.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	item.ProjectIDHash = env.ProjectIDHash()
	item.UpstreamOrigin = env.UpstreamOrigin()
	item.Endpoint = env.Endpoint()
	item.Garbled = env.Garbled()
	if ts := env.Timestamp(); !ts.IsZero() {
		item.Timestamp = ts.UTC().Format(time.RFC3339Nano)
	}
	return item
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
