package upload_test

import (
	"maps"
	"testing"

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
// prove only one half of the rule.
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
