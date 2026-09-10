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
// reaches the disposition the contract names — acknowledge, keep and
// retry, pause, pause and point at the authorization, or quarantine —
// and leaves behind on disk what that disposition promises. The fixture
// names the disposition in the client's own type, so there is no
// translation step here that could agree with neither side.
// What it does not prove: that this client *builds* an envelope matching
// the fixture's. That comparison belongs to the envelope's own tests;
// here the fixtures are read for their answers. The schema_version 2
// fixtures are additionally read for their shape, in conformance_v2_test.go.
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

	seen := map[upload.Disposition]bool{}
	versions := map[any]bool{}
	for _, c := range cases {
		seen[c.Meta.Expect] = true
		versions[c.Envelope["schema_version"]] = true
		t.Run(c.Name, func(t *testing.T) {
			f := newFixture(t)
			f.server.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
				body := map[string]any{}
				for k, v := range c.Response.Body {
					body[k] = v
				}
				if c.Meta.Expect == upload.Ack {
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
			assertContractRow(t, f, c, res)
			if c.Envelope["schema_version"] == "2" {
				assertV2Fixture(t, c)
			}
		})
	}

	// A fixture set that never exercises the pause rows would let this
	// whole layer pass while the two arms the contract spends the most
	// words on go untested.
	for want := range contractRows {
		if !seen[want] {
			t.Errorf("no fixture requires the %q disposition", want)
		}
	}
	// Both batch versions are in the contract; a set missing either
	// would let the shape checks above pass by never running.
	for _, want := range []string{"1", "2"} {
		if !versions[want] {
			t.Errorf("no fixture carries schema_version %q", want)
		}
	}
}

// One row per disposition: what the flush must report and what an
// outside observer must find on disk afterwards. Naming a disposition
// this client cannot reach is the same failure as taking the wrong one,
// so a fixture whose word is not a key here fails rather than skipping.
var contractRows = map[upload.Disposition]struct {
	spooled     bool
	quarantined bool
	outcome     upload.Outcome
	// orDeferred admits the second reading a row may honestly carry: a
	// disposition that asks for another attempt and also left a pause
	// behind reports that pause, on the flush that set it and on every
	// automatic flush after (reportDisposition). Only RetrySameID has
	// one, because only it covers both an answer that changes nothing —
	// a lost echo, a 500 — and answers whose remedy is a person rather
	// than time: 401 and 403 both pause. The contract's promise for the
	// row is the disposition and the records kept, so both readings
	// satisfy it; every other outcome still fails.
	orDeferred bool
}{
	upload.Ack:                   {outcome: upload.Uploaded},
	upload.RetrySameID:           {spooled: true, orDeferred: true},
	upload.PauseUploads:          {spooled: true, outcome: upload.UpgradeRequired},
	upload.PauseUploadsAuthorize: {spooled: true, outcome: upload.AuthorizationRequired},
	upload.Quarantine:            {quarantined: true, outcome: upload.Rejected},
}

func assertContractRow(t *testing.T, f *fixture, c conformance.Case, res upload.Result) {
	t.Helper()
	want, ok := contractRows[c.Meta.Expect]
	if !ok {
		t.Fatalf("fixture requires disposition %q, which this client does not implement — the contract grew a row", c.Meta.Expect)
	}
	if res.Disposition != c.Meta.Expect {
		t.Fatalf("disposition = %q, the contract requires %q", res.Disposition, c.Meta.Expect)
	}
	if res.Outcome != want.outcome && !(want.orDeferred && res.Outcome == upload.Deferred) {
		t.Errorf("outcome = %q, want %q", res.Outcome, want.outcome)
	}
	// Records kept and records quarantined are the two facts every row of
	// the contract is really about: a pause that quarantines locks a
	// user's data away over a condition they resolve elsewhere, and a
	// failure that drops records loses data the service never took.
	if spooled := f.spool.Usage() > 0; spooled != want.spooled {
		t.Errorf("records in the spool = %v, want %v", spooled, want.spooled)
	}
	if quarantined := rejectedRecords(t, f.rejected) > 0; quarantined != want.quarantined {
		t.Errorf("records in the quarantine = %v, want %v", quarantined, want.quarantined)
	}
	// The address is the whole of what the authorize row adds over the
	// pause above it: without it the user is told to go somewhere unnamed.
	if c.Meta.Expect == upload.PauseUploadsAuthorize {
		if url, ok := c.Response.Body["authorize_url"].(string); ok && url != "" && res.Standing.AuthorizeURL != url {
			t.Errorf("authorize URL = %q, want %q", res.Standing.AuthorizeURL, url)
		}
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
