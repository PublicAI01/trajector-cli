package platform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"time"
)

// BatchesPath is the batch upload endpoint.
const BatchesPath = "/v1/batches"

// uploadTimeout bounds one batch upload end to end. Batches are bounded
// by the flush threshold, so a minute is generous even on slow links.
const uploadTimeout = time.Minute

// Handshake is what the service tells the client alongside a batch
// acknowledgement: the minimum client version it will keep accepting,
// updated thresholds, and an optional notice for the user. Zero values
// mean the service left a setting alone.
type Handshake struct {
	MinClientVersion string `json:"min_client_version,omitempty"`
	FlushBytes       int64  `json:"flush_bytes,omitempty"`
	FlushAgeSeconds  int64  `json:"flush_age_seconds,omitempty"`
	SpoolQuotaBytes  int64  `json:"spool_quota_bytes,omitempty"`
	Notice           string `json:"notice,omitempty"`
}

// BatchAck is the service's answer to one accepted batch upload.
type BatchAck struct {
	BatchID   string
	Handshake Handshake
}

// UploadBatch posts one batch: the uncompressed envelope and the
// compressed records as two multipart parts, so the service can route
// on the envelope without unpacking the records. The returned
// acknowledgement is only trusted when it echoes the batch id — a 2xx
// that names no batch proves nothing was persisted, and the caller must
// keep its data.
func (c *Client) UploadBatch(deviceToken, batchID string, envelope, records []byte) (BatchAck, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := writePart(mw, "batch", "batch.json", "application/json", envelope); err != nil {
		return BatchAck{}, fmt.Errorf("platform: assembling batch upload: %w", err)
	}
	if err := writePart(mw, "records", "records.zst", "application/zstd", records); err != nil {
		return BatchAck{}, fmt.Errorf("platform: assembling batch upload: %w", err)
	}
	if err := mw.Close(); err != nil {
		return BatchAck{}, fmt.Errorf("platform: assembling batch upload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+BatchesPath, &body)
	if err != nil {
		return BatchAck{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+deviceToken)

	client := *c.http
	client.Timeout = uploadTimeout
	resp, err := client.Do(req)
	if err != nil {
		return BatchAck{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return BatchAck{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return BatchAck{}, fmt.Errorf("platform: POST %s: %s", BatchesPath, resp.Status)
	}

	var reply struct {
		BatchID string `json:"batch_id"`
		Handshake
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return BatchAck{}, fmt.Errorf("platform: decoding batch acknowledgement: %w", err)
	}
	if reply.BatchID != batchID {
		return BatchAck{}, fmt.Errorf("platform: acknowledgement names batch %q, uploaded %q", reply.BatchID, batchID)
	}
	return BatchAck{BatchID: reply.BatchID, Handshake: reply.Handshake}, nil
}

func writePart(mw *multipart.Writer, field, filename, contentType string, data []byte) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}
