package batch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

// readableVersions are the batch envelope versions this package reads
// back. A batch is written at envelope.SchemaVersion and never at any
// other; the older value is readable so an envelope written before an
// upgrade, or by the other side of the contract, still parses.
var readableVersions = map[string]bool{"2": true, envelope.SchemaVersion: true}

// Index is the batch envelope: it indexes every record in the stream by
// record_id and names each one's source. Every JSON tag is part of the
// documented contract, and the field order is the serialized order.
type Index struct {
	SchemaVersion string      `json:"schema_version"`
	BatchID       string      `json:"batch_id"`
	ClientVersion string      `json:"client_version"`
	CreatedAt     string      `json:"created_at"`
	Compression   string      `json:"compression"`
	RecordsSize   int64       `json:"records_size"`
	Records       []IndexItem `json:"records"`
	Run           Run         `json:"run"`
}

// IndexItem indexes one record inside the decompressed stream. Source
// is what lets a receiver route the record before decompressing it;
// UpstreamOrigin and Endpoint exist only for rawcalls. The metadata
// fields are copies of what the record's own envelope says.
type IndexItem struct {
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

// newIndex starts an empty envelope for one batch. Records and
// RecordsSize are filled by add as records are laid out.
func newIndex(id string, createdAt time.Time, clientVersion string, run Run) Index {
	return Index{
		SchemaVersion: envelope.SchemaVersion,
		BatchID:       id,
		ClientVersion: clientVersion,
		CreatedAt:     createdAt.UTC().Format(time.RFC3339Nano),
		Compression:   "zstd",
		Records:       []IndexItem{},
		Run:           run,
	}
}

// add appends an item at the current end of the stream, sized to the
// record's bytes, and grows the stream size to match.
func (ix *Index) add(item IndexItem, recordSize int64) {
	item.Offset = ix.RecordsSize
	item.Size = recordSize
	ix.Records = append(ix.Records, item)
	ix.RecordsSize += recordSize
}

// Bytes serializes the envelope.
func (ix Index) Bytes() ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ix); err != nil {
		return nil, fmt.Errorf("batch: serializing envelope: %w", err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// ParseIndex reads an envelope back, refusing any version this package
// cannot read and keeping the version the envelope declares: what it
// reads back is what was written, never a restatement of it.
func ParseIndex(data []byte) (Index, error) {
	var ix Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return Index{}, fmt.Errorf("batch: reading envelope: %w", err)
	}
	if !readableVersions[ix.SchemaVersion] {
		return Index{}, fmt.Errorf("batch: unsupported schema version %q", ix.SchemaVersion)
	}
	return ix, nil
}

// rawcallItem indexes a rawcall: its record id is its request id, and
// the item carries where the exchange went.
func rawcallItem(kind envelope.Kind, env envelope.Envelope) IndexItem {
	item := IndexItem{
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

// sessionRecordItem indexes one record of the second slot, whatever
// kind it is: every such record is indexed by the id and the capture it
// declares, and by nothing of its own kind. The source is the record's
// own declaration, so a receiver routes every kind of record by what
// that kind says it is.
func sessionRecordItem(kind envelope.Kind, recordID string, capture envelope.Capture) IndexItem {
	return IndexItem{
		RecordID:      recordID,
		Source:        kind.Source,
		ProjectIDHash: capture.ProjectIDHash,
		Timestamp:     capture.Timestamp,
	}
}
