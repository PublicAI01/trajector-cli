package upload_test

import (
	"os"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// The shared upload-contract fixtures, driven against the real uploader.
//
// The tests beside this one are built from fixtures written here, and
// the service has tests built from fixtures written there. Both sides
// can pass their own and still disagree with each other; only reading
// the same bytes catches that, and this is where this client reads them.
//
// What it proves: for each answer the contract describes, this client
// takes the disposition the contract names — acknowledge, keep and
// retry, pause, pause and point at the authorization, or quarantine.
// What it does not prove: that this client *builds* an envelope matching
// the fixture's. That comparison belongs to the envelope's own tests;
// here the fixtures are read for their answers.
//
// One substitution, and it is not a shortcut: for the acknowledged case
// the response's batch id is replaced with the id this client actually
// sent. The contract defines that row as "2xx echoing the batch id", and
// only the sender knows the id under test. Every other case is used
// verbatim — including the one whose whole point is that the echoed id
// is wrong.
func TestSharedContractFixtures(t *testing.T) {
	dir, tried := conformance.Find()
	if dir == "" {
		// Absent fixtures must never read as a pass. Everyone who checked
		// out only this repository lands here, so it is a skip with the
		// reason printed — and a failure in the one place they are certain
		// to exist.
		if os.Getenv(conformance.StrictEnv) == "1" {
			t.Fatalf("shared contract fixtures not found; looked in:\n  %s", join(tried))
		}
		t.Skipf("shared contract fixtures not found — this layer is not running.\nLooked in:\n  %s\nSet %s to point at them, or %s=1 to make this a failure.",
			join(tried), conformance.DirEnv, conformance.StrictEnv)
	}

	cases, err := conformance.Load(dir)
	if err != nil {
		t.Fatalf("loading fixtures from %s: %v", dir, err)
	}
	if len(cases) == 0 {
		t.Fatalf("no fixtures under %s — a layer that checks nothing must not report success", dir)
	}

	seen := map[string]bool{}
	for _, c := range cases {
		seen[c.Meta.Expect] = true
		t.Run(c.Name, func(t *testing.T) {
			f := newFixture(t)
			f.server.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
				body := map[string]any{}
				for k, v := range c.Response.Body {
					body[k] = v
				}
				if c.Meta.Expect == conformance.ExpectAck {
					body["batch_id"] = uploadedBatchID(t, r)
				}
				resp := fakeplatform.JSON(c.Response.Status, body)
				for k, v := range c.Response.Headers {
					resp.Header.Set(k, v)
				}
				return resp
			})
			f.storeRawcall(t, "req-1", time.Now().UTC())

			res, _ := f.uploader.Flush(true)
			assertDisposition(t, f, c, res)
		})
	}

	// A fixture set that never exercises the pause rows would let this
	// whole layer pass while the two arms the contract spends the most
	// words on go untested.
	for _, want := range []string{
		conformance.ExpectAck,
		conformance.ExpectRetrySameID,
		conformance.ExpectPauseUploads,
		conformance.ExpectPauseUploadsAuthorize,
		conformance.ExpectQuarantine,
	} {
		if !seen[want] {
			t.Errorf("no fixture requires the %q disposition", want)
		}
	}
}

func assertDisposition(t *testing.T, f *fixture, c conformance.Case, res upload.Result) {
	t.Helper()
	spooled := f.spool.Usage() > 0
	quarantined := rejectedRecords(t, f.rejected)

	switch c.Meta.Expect {
	case conformance.ExpectAck:
		if res.Outcome != upload.Uploaded {
			t.Fatalf("outcome = %q, want %q", res.Outcome, upload.Uploaded)
		}
		if spooled {
			t.Error("records survived an acknowledged upload")
		}
	case conformance.ExpectRetrySameID:
		if res.Outcome == upload.Uploaded {
			t.Fatal("a failure was taken as an acknowledgement")
		}
		if !spooled {
			t.Error("records were dropped by a failure the contract calls transient")
		}
		if quarantined != 0 {
			t.Errorf("%d record(s) quarantined by a transient failure", quarantined)
		}
	case conformance.ExpectPauseUploads:
		if res.Outcome != upload.UpgradeRequired {
			t.Fatalf("outcome = %q, want %q", res.Outcome, upload.UpgradeRequired)
		}
		assertKeptWhole(t, spooled, quarantined)
	case conformance.ExpectPauseUploadsAuthorize:
		if res.Outcome != upload.AuthorizationRequired {
			t.Fatalf("outcome = %q, want %q", res.Outcome, upload.AuthorizationRequired)
		}
		assertKeptWhole(t, spooled, quarantined)
		// The address is the whole of what this row adds over the one
		// above: without it the user is told to go somewhere unnamed.
		if url, ok := c.Response.Body["authorize_url"].(string); ok && url != "" && res.AuthorizeURL != url {
			t.Errorf("authorize URL = %q, want %q", res.AuthorizeURL, url)
		}
	case conformance.ExpectQuarantine:
		if res.Outcome != upload.Rejected {
			t.Fatalf("outcome = %q, want %q", res.Outcome, upload.Rejected)
		}
		if quarantined == 0 {
			t.Error("a poison batch left nothing in the quarantine")
		}
	default:
		t.Fatalf("fixture requires disposition %q, which this client does not implement — the contract grew a row", c.Meta.Expect)
	}
}

// assertKeptWhole is the half the two pause rows share: everything is
// kept and nothing is quarantined. Getting this wrong locks a user's
// data away over a condition they resolve elsewhere.
func assertKeptWhole(t *testing.T, spooled bool, quarantined int) {
	t.Helper()
	if !spooled {
		t.Error("records were dropped by a pause")
	}
	if quarantined != 0 {
		t.Errorf("%d record(s) quarantined by a pause", quarantined)
	}
}

func join(paths []string) string {
	out := ""
	for i, p := range paths {
		if i > 0 {
			out += "\n  "
		}
		out += p
	}
	return out
}
