package batch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

// schemaVersionV2 is the batch envelope that indexes records by
// record_id and names each one's source. The record stream, the
// compression, and the idempotency of batch_id are the same as in
// schema_version 1; only the index items differ.
const schemaVersionV2 = "2"

// IndexV2 is the schema_version 2 batch envelope. Every JSON tag is part
// of the documented contract, and the field order is the serialized
// order.
type IndexV2 struct {
	SchemaVersion string        `json:"schema_version"`
	BatchID       string        `json:"batch_id"`
	ClientVersion string        `json:"client_version"`
	CreatedAt     string        `json:"created_at"`
	Compression   string        `json:"compression"`
	RecordsSize   int64         `json:"records_size"`
	Records       []IndexItemV2 `json:"records"`
	Run           Run           `json:"run"`
}

// IndexItemV2 indexes one record inside the decompressed stream. Source
// is what lets a receiver route the record before decompressing it;
// UpstreamOrigin and Endpoint exist only for rawcalls. The metadata
// fields are copies of what the record's own envelope says.
type IndexItemV2 struct {
	RecordID       string `json:"record_id"`
	Source         string `json:"source"`
	ProjectIDHash  string `json:"project_id_hash,omitempty"`
	UpstreamOrigin string `json:"upstream_origin,omitempty"`
	Endpoint       string `json:"endpoint,omitempty"`
	Timestamp      string `json:"timestamp,omitempty"`
	Garbled        bool   `json:"garbled,omitempty"`
	Offset         int64  `json:"offset"`
	Size           int64  `json:"size"`
}

// newIndexV2 starts an empty schema_version 2 envelope for one batch.
// Records and RecordsSize are filled by add as records are laid out.
func newIndexV2(id string, createdAt time.Time, clientVersion string, run Run) IndexV2 {
	return IndexV2{
		SchemaVersion: schemaVersionV2,
		BatchID:       id,
		ClientVersion: clientVersion,
		CreatedAt:     createdAt.UTC().Format(time.RFC3339Nano),
		Compression:   "zstd",
		Records:       []IndexItemV2{},
		Run:           run,
	}
}

// add appends an item at the current end of the stream, sized to the
// record's bytes, and grows the stream size to match.
func (ix *IndexV2) add(item IndexItemV2, recordSize int64) {
	item.Offset = ix.RecordsSize
	item.Size = recordSize
	ix.Records = append(ix.Records, item)
	ix.RecordsSize += recordSize
}

// Bytes serializes the envelope.
func (ix IndexV2) Bytes() ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ix); err != nil {
		return nil, fmt.Errorf("batch: serializing envelope: %w", err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// ParseIndexV2 reads a schema_version 2 envelope back, refusing any
// other version.
func ParseIndexV2(data []byte) (IndexV2, error) {
	var ix IndexV2
	if err := json.Unmarshal(data, &ix); err != nil {
		return IndexV2{}, fmt.Errorf("batch: reading envelope: %w", err)
	}
	if ix.SchemaVersion != schemaVersionV2 {
		return IndexV2{}, fmt.Errorf("batch: unsupported schema version %q", ix.SchemaVersion)
	}
	return ix, nil
}

// rawcallItem indexes a rawcall: its record id is its request id, and
// the item carries where the exchange went.
func rawcallItem(kind envelope.Kind, env envelope.Envelope) IndexItemV2 {
	item := IndexItemV2{
		RecordID:       env.RequestID(),
		Source:         kind.Source,
		ProjectIDHash:  env.ProjectIDHash(),
		UpstreamOrigin: env.UpstreamOrigin(),
		Endpoint:       env.Endpoint(),
		Garbled:        env.Garbled(),
	}
	if ts := env.Timestamp(); !ts.IsZero() {
		item.Timestamp = ts.UTC().Format(time.RFC3339Nano)
	}
	return item
}

// segmentItem indexes one segment record.
func segmentItem(kind envelope.Kind, seg envelope.Segment) IndexItemV2 {
	return sessionRecordItem(kind, seg.RecordID, seg.Capture)
}

// metaSnapshotItem indexes one metadata snapshot record.
func metaSnapshotItem(kind envelope.Kind, snap envelope.MetaSnapshot) IndexItemV2 {
	return sessionRecordItem(kind, snap.RecordID, snap.Capture)
}

// sessionRecordItem indexes one record read from a session file. The
// source is the record's own declaration, so a receiver routes every
// kind of record by what that kind says it is.
func sessionRecordItem(kind envelope.Kind, recordID string, capture envelope.TranscriptCapture) IndexItemV2 {
	return IndexItemV2{
		RecordID:      recordID,
		Source:        kind.Source,
		ProjectIDHash: capture.ProjectIDHash,
		Timestamp:     capture.Timestamp,
	}
}
