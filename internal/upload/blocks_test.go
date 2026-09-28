package upload_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// constructedImage is a few dozen bytes of valid PNG, never a real
// screenshot.
func constructedImage(t *testing.T) string {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// storeCallRepeating stores a call of session sess-1 whose request
// repeats the image in a tool result, the way every call after a Read
// of it does. An empty payload stores a call with no image at all.
func (f *fixture) storeCallRepeating(t *testing.T, id, payload string) {
	t.Helper()
	content := `"hello"`
	if payload != "" {
		content = `[{"type":"tool_result","tool_use_id":"toolu_01","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + payload + `"}}]}]`
	}
	env, err := envelope.Record(envelope.Observation{
		Provider: "anthropic", Endpoint: "/v1/messages", HTTPStatus: 200, ClientVersion: "test",
		ProjectIDHash: "hash-p1", At: f.now,
		Upstream: "https://api.anthropic.com", OfficialUpstream: "https://api.anthropic.com",
		Request:          []byte(`{"model":"m","metadata":{"user_id":"sess-1"},"messages":[{"role":"user","content":` + content + `}]}`),
		RequestComplete:  true,
		Response:         []byte(`{"id":"` + id + `","type":"message"}`),
		ResponseComplete: true,
		ContentType:      "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.spool.Write(env); err != nil {
		t.Fatal(err)
	}
}

// uploadWith flushes everything stored and returns what that upload
// carried of the image: "original", "reference", or "placeholder".
func (f *fixture) uploadWith(t *testing.T, payload string) string {
	t.Helper()
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	reqs := f.server.Requests()
	stream := string(uploadedStream(t, reqs[len(reqs)-1]))
	switch {
	case strings.Contains(stream, payload):
		return "original"
	case strings.Contains(stream, `"type":"sha256_ref"`):
		return "reference"
	case strings.Contains(stream, `"type":"omitted"`):
		return "placeholder"
	}
	t.Fatalf("the upload carries no copy of the image")
	return ""
}

func TestFlush_SendsNoReferenceUntilTheServiceSaysItResolvesThem(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{}))
	x := constructedImage(t)

	f.storeCallRepeating(t, "req-1", x)
	f.storeCallRepeating(t, "req-2", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("first upload carried the image as %s", got)
	}
	f.storeCallRepeating(t, "req-3", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("with no word from the service, a repeated image went up as %s", got)
	}
	stream := string(uploadedStream(t, f.server.Requests()[0]))
	if strings.Count(stream, x) != 2 || strings.Contains(stream, "sha256_ref") {
		t.Fatal("a batch referred to its own earlier copy before the service said it resolves references")
	}
}

func TestFlush_RefersToAnImageThatAnAcknowledgedBatchCarried(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{BlockRefs: true}))
	x := constructedImage(t)

	f.storeCallRepeating(t, "req-1", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("first upload carried the image as %s", got)
	}
	f.storeCallRepeating(t, "req-2", x)
	if got := f.uploadWith(t, x); got != "reference" {
		t.Fatalf("a repeat of an acknowledged image went up as %s", got)
	}
	f.uploader = f.newUploader(t)
	f.storeCallRepeating(t, "req-3", x)
	if got := f.uploadWith(t, x); got != "reference" {
		t.Fatalf("after a restart, a repeat of an acknowledged image went up as %s", got)
	}
}

func TestFlush_RemembersAnImageOnlyOnceABatchCarryingItIsAcknowledged(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{BlockRefs: true}))
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(503, map[string]any{"error": "down"}))
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{BlockRefs: true}))
	x := constructedImage(t)

	f.storeCallRepeating(t, "req-0", "")
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	f.storeCallRepeating(t, "req-1", x)
	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("the flush against a failing service did not fail")
	}
	// The retry is acknowledged, but the service may be keeping the
	// failed attempt instead, so the retry proves nothing about the image.
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	f.storeCallRepeating(t, "req-2", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("an image no first attempt had acknowledged went up as %s", got)
	}
}

func TestFlush_SendsTheImageInFullAgainWhenTheRecordOfSentImagesIsLost(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{BlockRefs: true}))
	x := constructedImage(t)

	f.storeCallRepeating(t, "req-1", x)
	f.uploadWith(t, x)
	if err := os.Remove(filepath.Join(f.dir, "sent-blocks.json")); err != nil {
		t.Fatal(err)
	}
	f.uploader = f.newUploader(t)
	f.storeCallRepeating(t, "req-2", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("with the record of sent images lost, a repeat went up as %s", got)
	}
}

func TestFlush_StopsReferringWhenAnAcknowledgementNoLongerSaysSo(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{BlockRefs: true}))
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{FlushBytes: 1 << 20}))
	x := constructedImage(t)

	f.storeCallRepeating(t, "req-1", x)
	f.uploadWith(t, x)
	f.storeCallRepeating(t, "req-2", "")
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	f.storeCallRepeating(t, "req-3", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("after an acknowledgement that did not say it resolves references, a repeat went up as %s", got)
	}
}

func TestFlush_ReplacesImagesWithPlaceholdersWhileUploadIsTurnedOff(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{BlockRefs: true}))
	x := constructedImage(t)

	f.omitImages = true
	f.storeCallRepeating(t, "req-1", x)
	f.storeCallRepeating(t, "req-2", x)
	if got := f.uploadWith(t, x); got != "placeholder" {
		t.Fatalf("with upload turned off the image went up as %s", got)
	}
	stream := string(uploadedStream(t, f.server.Requests()[0]))
	if strings.Count(stream, `"type":"omitted"`) != 2 {
		t.Fatal("not every copy was replaced by a placeholder")
	}

	f.omitImages = false
	f.storeCallRepeating(t, "req-3", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("a placeholder is not the image; once upload is on again the image went up as %s", got)
	}
}

// sentAndRepeated uploads an image and then a repeat of it, and checks
// that the repeat went up as a reference: the state every test below
// starts its event from.
func (f *fixture) sentAndRepeated(t *testing.T, x string) {
	t.Helper()
	f.storeCallRepeating(t, "req-1", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("first upload carried the image as %s", got)
	}
	f.storeCallRepeating(t, "req-2", x)
	if got := f.uploadWith(t, x); got != "reference" {
		t.Fatalf("a repeat of an acknowledged image went up as %s", got)
	}
}

// A new device token can put the device under another account, where
// nothing this device sent before is held. The first batch under it
// refers to nothing; once the service acknowledges under the new token
// that it resolves references, repeats become references again.
func TestFlush_SendsTheImageInFullAgainUnderAnotherDeviceToken(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{BlockRefs: true}))
	x := constructedImage(t)
	f.sentAndRepeated(t, x)

	f.token = "dev-tok-another"
	f.storeCallRepeating(t, "req-3", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("under another device token a repeated image went up as %s", got)
	}
	f.storeCallRepeating(t, "req-4", x)
	if got := f.uploadWith(t, x); got != "reference" {
		t.Fatalf("after an acknowledgement under the new token a repeat went up as %s", got)
	}
}

// Another service address is another service: nothing the first one
// acknowledged is held there. A proxy started with a new address reads
// the same files.
func TestFlush_SendsTheImageInFullAgainToAnotherService(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{BlockRefs: true}))
	x := constructedImage(t)
	f.sentAndRepeated(t, x)

	f.server = fakeplatform.New(t)
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{BlockRefs: true}))
	f.uploader = f.newUploader(t)
	f.storeCallRepeating(t, "req-3", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("to another service a repeated image went up as %s", got)
	}
}

// ForgetSentBlocks runs in another process than the uploader, which
// keeps what it read of the record in memory. The next batch of the
// running uploader still refers to nothing.
func TestFlush_SendsTheImageInFullAgainOnceAnotherProcessForgetsWhatWasSent(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.EchoAck(platform.Handshake{BlockRefs: true}))
	x := constructedImage(t)
	f.sentAndRepeated(t, x)

	if err := upload.ForgetSentBlocks(f.dir); err != nil {
		t.Fatal(err)
	}
	f.storeCallRepeating(t, "req-3", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("after the record was forgotten a repeated image went up as %s", got)
	}
	f.storeCallRepeating(t, "req-4", x)
	if got := f.uploadWith(t, x); got != "reference" {
		t.Fatalf("after a later acknowledgement a repeat went up as %s", got)
	}
}

// The event can come while a batch is on its way. The service may have
// taken away what that batch carried as well, so its payloads are not
// remembered, although it is acknowledged.
func TestFlush_RemembersNothingOfABatchAcknowledgedAfterTheRecordWasForgotten(t *testing.T) {
	f := newFixture(t)
	echo := fakeplatform.EchoAck(platform.Handshake{BlockRefs: true})
	forgetting := false
	f.server.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		if forgetting {
			if err := upload.ForgetSentBlocks(f.dir); err != nil {
				t.Error(err)
			}
		}
		return echo(r)
	})
	x := constructedImage(t)
	f.storeCallRepeating(t, "req-0", "")
	if got, err := f.uploader.Flush(true); err != nil || got.Outcome != upload.Uploaded {
		t.Fatalf("Flush = %+v, %v", got, err)
	}

	forgetting = true
	f.storeCallRepeating(t, "req-1", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("first upload carried the image as %s", got)
	}
	forgetting = false
	f.storeCallRepeating(t, "req-2", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Fatalf("a repeat of an image acknowledged after the record was forgotten went up as %s", got)
	}
}
