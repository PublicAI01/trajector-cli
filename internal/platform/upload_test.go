package platform_test

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/platform"
)

func ackStub(status int, batchID string) fakeplatform.Response {
	return fakeplatform.JSON(status, map[string]any{
		"batch_id":           batchID,
		"min_client_version": "0.9.0",
		"flush_bytes":        1024,
		"flush_age_seconds":  3600,
		"spool_quota_bytes":  4096,
		"notice":             "please upgrade",
	})
}

func TestUploadBatchPostsEnvelopeAndRecordsAsMultipart(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/batches", ackStub(200, "batch-1"))

	ack, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte(`{"batch_id":"batch-1"}`), []byte("zstd-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if ack.BatchID != "batch-1" {
		t.Errorf("ack batch id = %q", ack.BatchID)
	}
	want := platform.Handshake{
		MinClientVersion: "0.9.0",
		FlushBytes:       1024,
		FlushAgeSeconds:  3600,
		SpoolQuotaBytes:  4096,
		Notice:           "please upgrade",
	}
	if ack.Handshake != want {
		t.Errorf("handshake = %+v, want %+v", ack.Handshake, want)
	}

	reqs := server.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	req := reqs[0]
	if got := req.Header.Get("Authorization"); got != "Bearer dev-tok-fake" {
		t.Errorf("authorization = %q", got)
	}
	parts := readParts(t, req)
	if string(parts["batch"]) != `{"batch_id":"batch-1"}` {
		t.Errorf("batch part = %q", parts["batch"])
	}
	if string(parts["records"]) != "zstd-bytes" {
		t.Errorf("records part = %q", parts["records"])
	}
}

func TestUploadBatchRejectsAnAckNamingAnotherBatch(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/batches", ackStub(200, "batch-other"))
	if _, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), []byte("z")); err == nil {
		t.Error("acknowledgement for another batch accepted")
	}
}

func TestUploadBatchRejectsAnAckNamingNoBatch(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/batches", fakeplatform.JSON(200, map[string]any{}))
	if _, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), []byte("z")); err == nil {
		t.Error("acknowledgement without a batch id accepted")
	}
}

func TestUploadBatchSurfacesServiceFailure(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/batches", ackStub(503, "batch-1"))
	if _, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), []byte("z")); err == nil {
		t.Error("503 response did not fail")
	}
}

func readParts(t *testing.T, req fakeplatform.Request) map[string][]byte {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("content type = %q (%v)", req.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(bytes.NewReader(req.Body), params["boundary"])
	parts := map[string][]byte{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading multipart: %v", err)
		}
		data, err := io.ReadAll(p)
		if err != nil {
			t.Fatalf("reading part %q: %v", p.FormName(), err)
		}
		parts[p.FormName()] = data
	}
	return parts
}
