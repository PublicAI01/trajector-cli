package platform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/redact"
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

// UpgradeRequiredError reports a 426: the service refuses uploads from
// this client version. Nothing is wrong with the data; the caller keeps
// it and stops offering it until the client is upgraded. It refines the
// underlying StatusError rather than replacing it, so errors.As still
// reaches the status and Temporary stays answerable.
type UpgradeRequiredError struct {
	MinClientVersion string
	status           *StatusError
}

func (e *UpgradeRequiredError) Error() string {
	if e.MinClientVersion == "" {
		return "the service requires a newer client version"
	}
	return "the service requires client version " + e.MinClientVersion + " or newer"
}

func (e *UpgradeRequiredError) Unwrap() error {
	if e.status == nil {
		return nil
	}
	return e.status
}

// MaxRetryAfter caps how long a Retry-After can silence automatic
// flushes, so a service misconfiguration cannot mute every client
// indefinitely. The cap is applied once, where the 429 is classified,
// so the pause the uploader takes and the one the error message reports
// can never drift apart.
const MaxRetryAfter = time.Hour

// RateLimitedError reports a 429. RetryAfter is the pause the client
// will actually take — the service's request capped at MaxRetryAfter —
// and zero when the service named none. It refines the underlying
// StatusError; unwrapping reaches a Temporary that answers true.
type RateLimitedError struct {
	RetryAfter time.Duration
	status     *StatusError
}

func (e *RateLimitedError) Error() string {
	if e.RetryAfter <= 0 {
		return "the service asked to slow down"
	}
	return fmt.Sprintf("the service asked to retry after %s", e.RetryAfter)
}

func (e *RateLimitedError) Unwrap() error {
	if e.status == nil {
		return nil
	}
	return e.status
}

// BatchRejectedError reports a 4xx that is neither auth, timeout,
// version, nor rate limiting: the service says this batch will never be
// accepted as it stands. It refines the underlying StatusError.
type BatchRejectedError struct {
	Status  string
	Details string
	status  *StatusError
}

func (e *BatchRejectedError) Error() string {
	msg := "the service rejected the batch: " + e.Status
	if e.Details != "" {
		msg += ": " + e.Details
	}
	return msg
}

func (e *BatchRejectedError) Unwrap() error {
	if e.status == nil {
		return nil
	}
	return e.status
}

// UploadBatch posts one batch: the uncompressed envelope and the
// compressed records as two multipart parts, so the service can route
// on the envelope without unpacking the records. The records parameter
// accepts only data certified by the redaction pass: this signature is
// what makes "unredacted data never leaves the machine" a compile-time
// fact. The returned acknowledgement is only trusted when it echoes the
// batch id — a 2xx that names no batch proves nothing was persisted,
// and the caller must keep its data.
func (c *Client) UploadBatch(deviceToken, batchID string, envelope []byte, records redact.RedactedBytes) (BatchAck, error) {
	if c.initErr != nil {
		return BatchAck{}, c.initErr
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := writePart(mw, "batch", "batch.json", "application/json", envelope); err != nil {
		return BatchAck{}, fmt.Errorf("platform: assembling batch upload: %w", err)
	}
	if err := writePart(mw, "records", "records.zst", "application/zstd", records.Bytes()); err != nil {
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
		return BatchAck{}, classifyUploadFailure(resp, data)
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

// classifyUploadFailure maps a non-2xx batch upload response onto the
// failure classes the uploader acts on. Auth failures and timeouts stay
// plain errors — transient, keep and retry — matching how every other
// failure without its own class is treated.
func classifyUploadFailure(resp *http.Response, body []byte) error {
	status := &StatusError{StatusCode: resp.StatusCode, Status: resp.Status, Method: http.MethodPost, Path: BatchesPath, Body: body}
	switch {
	case resp.StatusCode == http.StatusUpgradeRequired:
		var deny struct {
			MinClientVersion string `json:"min_client_version"`
		}
		// Best-effort detail: a 426 without a parseable body still gates.
		_ = json.Unmarshal(body, &deny)
		return &UpgradeRequiredError{MinClientVersion: deny.MinClientVersion, status: status}
	case resp.StatusCode == http.StatusTooManyRequests:
		pause := retryAfter(resp.Header.Get("Retry-After"))
		if pause > MaxRetryAfter {
			pause = MaxRetryAfter
		}
		return &RateLimitedError{RetryAfter: pause, status: status}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusRequestTimeout:
		return status
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return &BatchRejectedError{Status: resp.Status, Details: trimDetails(body), status: status}
	default:
		return status
	}
}

// retryAfter reads a Retry-After value in either of its two forms:
// delta-seconds or an HTTP date. Absent or unreadable reads as zero —
// no requested pause.
func retryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

func trimDetails(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 500 {
		s = s[:500]
	}
	return s
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
