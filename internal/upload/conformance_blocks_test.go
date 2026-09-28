package upload_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// An acknowledgement decides whether the next batch may carry
// references to images an earlier batch carried in full. Each shared
// fixture that acknowledges a batch is used as that acknowledgement,
// verbatim apart from the echoed batch id: the batch after it carries a
// repeated image as a reference exactly when the fixture's answer says
// "block_refs": true. A fixture set without both kinds of answer would
// prove only one half of the rule. A batch with no image goes first, so
// the service has stated its epoch before the image goes up.
func TestSharedFixturesDecideWhetherTheNextBatchRefersToImages(t *testing.T) {
	seen := map[bool]bool{}
	for _, c := range sharedFixtures(t) {
		if c.Meta.Expect != upload.Ack {
			continue
		}
		says, _ := c.Response.Body["block_refs"].(bool)
		seen[says] = true
		t.Run(c.Name, func(t *testing.T) {
			f := newFixture(t)
			f.server.StubFunc("POST", "/v1/batches", fixtureAck(t, c))
			x := constructedImage(t)
			f.acknowledgedOnce(t)

			f.storeCallRepeating(t, "req-1", x)
			if got := f.uploadWith(t, x); got != "original" {
				t.Fatalf("first upload carried the image as %s", got)
			}
			f.storeCallRepeating(t, "req-2", x)
			want := "original"
			if says {
				want = "reference"
			}
			if got := f.uploadWith(t, x); got != want {
				t.Errorf("after this acknowledgement a repeated image went up as %s, want %s", got, want)
			}
		})
	}
	for _, says := range []bool{true, false} {
		if !seen[says] {
			t.Errorf("no acknowledging fixture with block_refs=%v", says)
		}
	}
}

// fixtureAck answers every batch with the fixture's body, the echoed
// batch id being the one this client sent.
func fixtureAck(t *testing.T, c conformance.Case) func(fakeplatform.Request) fakeplatform.Response {
	t.Helper()
	return func(r fakeplatform.Request) fakeplatform.Response {
		body := maps.Clone(c.Response.Body)
		body["batch_id"] = uploadedBatchID(t, r)
		return fakeplatform.JSON(c.Response.Status, body)
	}
}

// Some shared fixtures carry a stream in which a record refers to an
// image that an earlier record of the batch carries in full, and give
// that record as it was recorded, before the batch rewrote it. Stored
// as recorded and uploaded after an acknowledgement that said
// "block_refs": true, with no image acknowledged before, the records
// must go up as the fixture's stream, byte for byte: each copy the
// stream carries in full goes up in full, and each reference in it is
// the one this client writes. The service reads the same stream and
// resolves every reference in it in the session it places the record
// in.
func TestSharedFixturesUploadTheRecordedCallsAsTheirStream(t *testing.T) {
	checked := 0
	for _, c := range sharedFixtures(t) {
		if c.Restored == nil {
			continue
		}
		checked++
		t.Run(c.Name, func(t *testing.T) {
			if says, _ := c.Response.Body["block_refs"].(bool); !says {
				t.Fatal("the fixture's answer does not say block_refs: true, so no batch after it could refer to an image")
			}
			f := newFixture(t)
			f.server.StubFunc("POST", "/v1/batches", fixtureAck(t, c))
			f.storeCallRepeating(t, "req-0", "")
			if _, err := f.uploader.Flush(true); err != nil {
				t.Fatalf("Flush: %v", err)
			}

			recorded := map[string][]byte{}
			for _, r := range c.Restored {
				recorded[requestIDOf(t, r)] = r
			}
			var want bytes.Buffer
			for _, r := range c.Records {
				want.Write(r)
				raw := r
				if rec, ok := recorded[requestIDOf(t, r)]; ok {
					raw = rec
				}
				env, err := envelope.Parse(raw)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.spool.Write(env); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.uploader.Flush(true); err != nil {
				t.Fatalf("Flush: %v", err)
			}
			reqs := f.server.Requests()
			if got := uploadedStream(t, reqs[len(reqs)-1]); !bytes.Equal(got, want.Bytes()) {
				t.Errorf("uploaded stream differs from the fixture's:\n got %s\nwant %s", got, want.Bytes())
			}
		})
	}
	if checked == 0 {
		t.Error("no fixture gives a record as it was recorded")
	}
}

func requestIDOf(t *testing.T, record []byte) string {
	t.Helper()
	var v struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(record, &v); err != nil || v.RequestID == "" {
		t.Fatalf("a fixture record names no request id: %v", err)
	}
	return v.RequestID
}

// acknowledgingFixtures are the shared fixtures that acknowledge a
// batch, by what their answer says about block references.
func acknowledgingFixtures(t *testing.T) (refs, withheld []conformance.Case) {
	t.Helper()
	for _, c := range sharedFixtures(t) {
		if c.Meta.Expect != upload.Ack {
			continue
		}
		if _, ok := c.Response.Body["block_refs_epoch"].(string); !ok {
			continue
		}
		if says, _ := c.Response.Body["block_refs"].(bool); says {
			refs = append(refs, c)
		} else {
			withheld = append(withheld, c)
		}
	}
	return refs, withheld
}

func epochOf(c conformance.Case) string {
	epoch, _ := c.Response.Body["block_refs_epoch"].(string)
	return epoch
}

// switchingAck answers with the fixture *current points at.
func switchingAck(t *testing.T, current **conformance.Case) func(fakeplatform.Request) fakeplatform.Response {
	return func(r fakeplatform.Request) fakeplatform.Response {
		return fixtureAck(t, **current)(r)
	}
}

// Two shared answers that say "block_refs": true in different epochs
// are the service before and after a deletion made on its side. After
// the answer in the new epoch, a repeated image goes up in full again.
func TestSharedFixturesInANewEpochMakeTheNextBatchSendImagesInFull(t *testing.T) {
	refs, _ := acknowledgingFixtures(t)
	var before, after *conformance.Case
	for i := range refs {
		for j := range refs {
			if epochOf(refs[i]) != epochOf(refs[j]) {
				before, after = &refs[i], &refs[j]
			}
		}
	}
	if before == nil {
		t.Fatal("no two acknowledging fixtures say block_refs: true in different epochs")
	}
	f := newFixture(t)
	current := before
	f.server.StubFunc("POST", "/v1/batches", switchingAck(t, &current))
	x := constructedImage(t)
	f.acknowledgedOnce(t)
	f.sentAndRepeated(t, x)

	current = after
	f.storeCallRepeating(t, "req-3", x)
	f.uploadWith(t, x)
	f.storeCallRepeating(t, "req-4", x)
	if got := f.uploadWith(t, x); got != "original" {
		t.Errorf("after %s answered in epoch %q, following %s in epoch %q, a repeated image went up as %s",
			after.Name, epochOf(*after), before.Name, epochOf(*before), got)
	}
}

// A shared answer that states the epoch and withholds block_refs keeps
// what an earlier batch had acknowledged in the same epoch, and adds
// nothing of its own batch.
func TestSharedFixturesThatWithholdBlockRefsKeepWhatWasSent(t *testing.T) {
	refs, withheld := acknowledgingFixtures(t)
	if len(withheld) == 0 {
		t.Fatal("no acknowledging fixture states an epoch and withholds block_refs")
	}
	for _, w := range withheld {
		t.Run(w.Name, func(t *testing.T) {
			i := slices.IndexFunc(refs, func(c conformance.Case) bool { return epochOf(c) == epochOf(w) })
			if i < 0 {
				t.Fatalf("no fixture says block_refs: true in epoch %q", epochOf(w))
			}
			f := newFixture(t)
			current := &refs[i]
			f.server.StubFunc("POST", "/v1/batches", switchingAck(t, &current))
			x, y := constructedImage(t), anotherImage(t)
			f.acknowledgedOnce(t)
			f.sentAndRepeated(t, x)

			current = &w
			f.storeCallRepeating(t, "req-3", y)
			f.uploadWith(t, y)
			current = &refs[i]
			f.storeCallRepeating(t, "req-4", "")
			if got, err := f.uploader.Flush(true); err != nil || got.Outcome != upload.Uploaded {
				t.Fatalf("Flush = %+v, %v", got, err)
			}
			f.storeCallRepeating(t, "req-5", x)
			if got := f.uploadWith(t, x); got != "reference" {
				t.Errorf("an image acknowledged before %s went up as %s", w.Name, got)
			}
			f.storeCallRepeating(t, "req-6", y)
			if got := f.uploadWith(t, y); got != "original" {
				t.Errorf("an image of the batch %s answered went up as %s", w.Name, got)
			}
		})
	}
}
