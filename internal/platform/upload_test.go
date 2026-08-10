package platform_test

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/redact"
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

func TestEveryUploadFailureClassCarriesItsStatus(t *testing.T) {
	// One taxonomy: every refined error class unwraps to the underlying
	// StatusError, so errors.As always reaches a status and Temporary is
	// answerable for every service failure.
	tests := []struct {
		name      string
		response  fakeplatform.Response
		temporary bool
	}{
		{"426 upgrade required", fakeplatform.JSON(426, map[string]any{"min_client_version": "9.9.9"}), false},
		{"429 rate limited", fakeplatform.Response{Status: 429, Body: []byte(`{}`)}, true},
		{"400 batch rejected", fakeplatform.JSON(400, map[string]any{"error": "malformed"}), false},
		{"401 unauthorized", fakeplatform.JSON(401, map[string]any{}), false},
		{"503 service failure", fakeplatform.JSON(503, map[string]any{}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, server := client(t)
			server.Stub("POST", "/v1/batches", tt.response)

			_, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), redact.AlreadyRedacted([]byte("z")), 0)
			if err == nil {
				t.Fatal("upload succeeded against a failing service")
			}
			var status *platform.StatusError
			if !errors.As(err, &status) {
				t.Fatalf("errors.As(%T, *StatusError) failed: %v", err, err)
			}
			if status.Temporary() != tt.temporary {
				t.Errorf("Temporary() = %v, want %v for %v", status.Temporary(), tt.temporary, err)
			}
		})
	}
}

func TestUploadBatchPostsEnvelopeAndRecordsAsMultipart(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/batches", ackStub(200, "batch-1"))

	ack, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte(`{"batch_id":"batch-1"}`), redact.AlreadyRedacted([]byte("zstd-bytes")), 0)
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
	if _, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), redact.AlreadyRedacted([]byte("z")), 0); err == nil {
		t.Error("acknowledgement for another batch accepted")
	}
}

func TestUploadBatchRejectsAnAckNamingNoBatch(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/batches", fakeplatform.JSON(200, map[string]any{}))
	if _, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), redact.AlreadyRedacted([]byte("z")), 0); err == nil {
		t.Error("acknowledgement without a batch id accepted")
	}
}

func TestUploadBatchSurfacesServiceFailure(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/batches", ackStub(503, "batch-1"))
	if _, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), redact.AlreadyRedacted([]byte("z")), 0); err == nil {
		t.Error("503 response did not fail")
	}
}

func upload4xx(t *testing.T, status int, header http.Header, body map[string]any) error {
	t.Helper()
	c, server := client(t)
	resp := fakeplatform.JSON(status, body)
	for k, vs := range header {
		resp.Header[k] = vs
	}
	server.Stub("POST", "/v1/batches", resp)
	_, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), redact.AlreadyRedacted([]byte("z")), 0)
	if err == nil {
		t.Fatalf("status %d did not fail", status)
	}
	return err
}

func TestUploadBatch426ReportsUpgradeRequired(t *testing.T) {
	err := upload4xx(t, 426, nil, map[string]any{"min_client_version": "2.0.0"})
	var upgrade *platform.UpgradeRequiredError
	if !errors.As(err, &upgrade) || upgrade.MinClientVersion != "2.0.0" {
		t.Fatalf("error = %v, want UpgradeRequiredError with the version", err)
	}
}

func TestUploadBatch426CarriesTheServiceWording(t *testing.T) {
	// A version number cannot say why the upgrade is required or by
	// when. Without the message the client can only relay a number.
	err := upload4xx(t, 426, nil, map[string]any{
		"min_client_version": "2.0.0",
		"message":            "the 0.1.x upload format is no longer accepted",
	})
	var upgrade *platform.UpgradeRequiredError
	if !errors.As(err, &upgrade) || upgrade.Message != "the 0.1.x upload format is no longer accepted" {
		t.Fatalf("error = %v, message = %q", err, upgrade.Message)
	}
}

func TestUploadBatch426WithoutAMessageSaysNothingExtra(t *testing.T) {
	err := upload4xx(t, 426, nil, map[string]any{"min_client_version": "2.0.0"})
	var upgrade *platform.UpgradeRequiredError
	if !errors.As(err, &upgrade) || upgrade.Message != "" {
		t.Fatalf("error = %v, message = %q, want empty so no line is printed", err, upgrade.Message)
	}
}

func TestUploadBatch426WordingCannotDrawOnTheTerminal(t *testing.T) {
	// The 426 body is service-controlled text that lands on a terminal
	// line beside the client's own words. It is cleaned here, at the
	// boundary, not at each place that prints it.
	err := upload4xx(t, 426, nil, map[string]any{
		"min_client_version": "2.0.0\x1b[2J",
		"message":            "upgrade\r\x1b[Atrajector: everything is fine",
	})
	var upgrade *platform.UpgradeRequiredError
	if !errors.As(err, &upgrade) {
		t.Fatalf("error = %v", err)
	}
	if strings.ContainsAny(upgrade.Message, "\x1b\r\n") || strings.ContainsAny(upgrade.MinClientVersion, "\x1b\r\n") {
		t.Fatalf("version = %q, message = %q, want both disarmed", upgrade.MinClientVersion, upgrade.Message)
	}
}

func TestUploadBatchAckWordingCannotDrawOnTheTerminal(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/batches", fakeplatform.JSON(200, map[string]any{
		"batch_id": "batch-1",
		"notice":   "scheduled\x1b[2J\rmaintenance",
	}))
	ack, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), redact.AlreadyRedacted([]byte("z")), 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(ack.Handshake.Notice, "\x1b\r\n") {
		t.Fatalf("notice = %q, want it disarmed", ack.Handshake.Notice)
	}
}

func TestUploadBatch429CarriesRetryAfterSeconds(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "7")
	err := upload4xx(t, 429, h, map[string]any{})
	var limited *platform.RateLimitedError
	if !errors.As(err, &limited) || limited.RetryAfter != 7*time.Second {
		t.Fatalf("error = %v, want RateLimitedError with 7s", err)
	}
}

func TestUploadBatch429CarriesRetryAfterDate(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat))
	err := upload4xx(t, 429, h, map[string]any{})
	var limited *platform.RateLimitedError
	if !errors.As(err, &limited) || limited.RetryAfter <= 0 || limited.RetryAfter > time.Minute {
		t.Fatalf("error = %v, want RateLimitedError with a positive pause under a minute", err)
	}
}

func TestUploadBatch429WithoutRetryAfterAsksNoPause(t *testing.T) {
	err := upload4xx(t, 429, nil, map[string]any{})
	var limited *platform.RateLimitedError
	if !errors.As(err, &limited) || limited.RetryAfter != 0 {
		t.Fatalf("error = %v, want RateLimitedError without a pause", err)
	}
}

func TestUploadBatchOther4xxReportsRejection(t *testing.T) {
	for _, status := range []int{400, 413, 422} {
		err := upload4xx(t, status, nil, map[string]any{"error": "bad multipart"})
		var rejected *platform.BatchRejectedError
		if !errors.As(err, &rejected) || !strings.Contains(rejected.Details, "bad multipart") {
			t.Fatalf("status %d error = %v, want BatchRejectedError with details", status, err)
		}
	}
}

func TestUploadBatchAuthAndTimeoutFailuresStayTransient(t *testing.T) {
	for _, status := range []int{401, 408} {
		err := upload4xx(t, status, nil, map[string]any{})
		var rejected *platform.BatchRejectedError
		var upgrade *platform.UpgradeRequiredError
		var limited *platform.RateLimitedError
		if errors.As(err, &rejected) || errors.As(err, &upgrade) || errors.As(err, &limited) {
			t.Fatalf("status %d error = %v, want a plain transient error", status, err)
		}
	}
}

func TestUploadBudgetScalesWithBatchSizeAndConsecutiveTimeouts(t *testing.T) {
	if got := platform.UploadBudget(0, 0); got != time.Minute {
		t.Errorf("budget for an empty body = %s, want the 1m0s floor", got)
	}
	if got := platform.UploadBudget(60*(64<<10), 0); got != 2*time.Minute {
		t.Errorf("budget for a minute of bytes at the floor rate = %s, want 2m0s", got)
	}
	if small, large := platform.UploadBudget(1<<20, 0), platform.UploadBudget(32<<20, 0); large <= small {
		t.Errorf("budget did not grow with batch size: %s for 1 MiB, %s for 32 MiB", small, large)
	}
	if got := platform.UploadBudget(0, 2); got != 4*time.Minute {
		t.Errorf("budget after two timeouts = %s, want 4m0s", got)
	}
	if got := platform.UploadBudget(1<<40, 50); got != 30*time.Minute {
		t.Errorf("budget for an outsized batch after many timeouts = %s, want the 30m0s cap", got)
	}
}

func TestUploadBatchTimeoutNamesTheBatchSizeInsteadOfABareDeadline(t *testing.T) {
	c, server := client(t)
	server.StubFunc("POST", "/v1/batches", func(fakeplatform.Request) fakeplatform.Response {
		time.Sleep(300 * time.Millisecond)
		return ackStub(200, "batch-1")
	})
	server.Stub("POST", "/v1/batches", ackStub(200, "batch-1"))

	_, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), redact.AlreadyRedacted([]byte("z")), 50*time.Millisecond)
	var timedOut *platform.UploadTimeoutError
	if !errors.As(err, &timedOut) {
		t.Fatalf("error = %v, want UploadTimeoutError", err)
	}
	if timedOut.BatchBytes <= 0 {
		t.Errorf("BatchBytes = %d, want the upload body size", timedOut.BatchBytes)
	}
	msg := err.Error()
	if !strings.Contains(msg, "B)") || !strings.Contains(msg, "too slow") {
		t.Errorf("error %q does not name the batch size and the slow connection", msg)
	}
	if strings.Contains(msg, "context deadline exceeded") {
		t.Errorf("error %q surfaces the bare deadline", msg)
	}

	if _, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), redact.AlreadyRedacted([]byte("z")), 0); err != nil {
		t.Errorf("upload with the default budget = %v, want success once the response arrives", err)
	}
}

func TestUploadBatch408ReportsATimedOutUploadThatStaysTransient(t *testing.T) {
	err := upload4xx(t, 408, nil, map[string]any{})
	var timedOut *platform.UploadTimeoutError
	if !errors.As(err, &timedOut) || timedOut.BatchBytes <= 0 {
		t.Fatalf("error = %v, want UploadTimeoutError naming the batch size", err)
	}
	var status *platform.StatusError
	if !errors.As(err, &status) || !status.Temporary() {
		t.Fatalf("error = %v, want a temporary status underneath", err)
	}
}

func TestHumanBytesRendersBinaryUnits(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{10 << 20, "10.0 MiB"},
		{2 << 30, "2.0 GiB"},
		{3 << 40, "3.0 TiB"},
	}
	for _, tt := range tests {
		if got := platform.HumanBytes(tt.n); got != tt.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
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

func TestARejectionBodyCannotDrawItsOwnLinesOnTheTerminal(t *testing.T) {
	// The rejection detail is printed by upload, printed again by
	// doctor among its own ok:/problem: verdicts, kept on the
	// quarantined batch and shipped in the diagnostic bundle. It is
	// free text the service wrote, so it is disarmed where it enters
	// the process, like every other sentence the service supplies.
	c, server := client(t)
	server.Stub("POST", "/v1/batches", fakeplatform.Response{
		Status: 400,
		Body:   []byte("rejected\r\x1b[2K  ok: everything checks out\n  ok: nothing to do"),
	})
	_, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), redact.AlreadyRedacted([]byte("z")), 0)
	var rejected *platform.BatchRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("error = %v, want BatchRejectedError", err)
	}
	if strings.ContainsAny(rejected.Details, "\r\n\x1b") {
		t.Fatalf("details = %q, want no rune that can move the cursor or start a line", rejected.Details)
	}
	if !strings.Contains(rejected.Details, "rejected") {
		t.Errorf("details = %q, want the words the service wrote still readable", rejected.Details)
	}
}

func TestALongRejectionBodyIsCutWithoutBreakingACharacter(t *testing.T) {
	c, server := client(t)
	server.Stub("POST", "/v1/batches", fakeplatform.Response{
		Status: 400,
		Body:   []byte(strings.Repeat("界", 8192)),
	})
	_, err := c.UploadBatch("dev-tok-fake", "batch-1", []byte("{}"), redact.AlreadyRedacted([]byte("z")), 0)
	var rejected *platform.BatchRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("error = %v, want BatchRejectedError", err)
	}
	if !utf8.ValidString(rejected.Details) {
		t.Fatalf("details = %q, want valid UTF-8 after the cut", rejected.Details)
	}
	if n := utf8.RuneCountInString(rejected.Details); n > 512 {
		t.Fatalf("details kept %d runes, want a detail that cannot fill the screen", n)
	}
}
