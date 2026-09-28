package mediablock_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
	"github.com/PublicAI01/trajector-cli/internal/mediablock"
)

// The shared fixtures state, for each record a batch packs, the bytes
// the receiving service must get: the same fixtures the service reads
// to check that it puts each reference back. One pass per case packs
// the case's records in order, each under the scope of its recording
// path, and every rewritten record must equal the fixture byte for
// byte. The scope here is the recording path alone: these fixtures check
// the rewrite, and the scope a batch computes for each record is checked
// through the uploader by TestSharedFixturesUploadTheRecordedCallsAsTheirStream.
func TestSharedBlockFixturesRewriteEveryRecordToTheExpectedBytes(t *testing.T) {
	for _, c := range sharedBlockFixtures(t) {
		t.Run(c.Name, func(t *testing.T) {
			pass := mediablock.NewPass(policyOf(c.Meta))
			for i, r := range c.Records {
				out := pass.Rewrite(r.Path, r.Input)
				if string(out.Data) != string(r.Expect) {
					t.Errorf("record %d (%s):\n got %s\nwant %s", i+1, r.Path, out.Data, r.Expect)
				}
				pass.Keep(out)
			}
		})
	}
}

// policyOf is the policy a fixture's batch is built under. A service
// that has not said it puts references back gets none, whatever this
// client sent before.
func policyOf(m conformance.BlocksMeta) mediablock.Policy {
	p := mediablock.Policy{Omit: m.Omit}
	if m.BlockRefs {
		p.Sent = func(_, digest string) bool { return slices.Contains(m.Sent, digest) }
	}
	return p
}

func sharedBlockFixtures(t *testing.T) []conformance.BlocksCase {
	t.Helper()
	dir, tried := conformance.Find()
	if dir == "" {
		if os.Getenv(conformance.StrictEnv) == "1" {
			t.Fatalf("shared contract fixtures not found; looked in:\n  %s", strings.Join(tried, "\n  "))
		}
		t.Skipf("shared contract fixtures not found — this layer is not running.\nLooked in:\n  %s\nSet %s to point at them, or %s=1 to make this a failure.",
			strings.Join(tried, "\n  "), conformance.DirEnv, conformance.StrictEnv)
	}
	cases, err := conformance.LoadBlocks(dir)
	if err != nil {
		t.Fatalf("loading block fixtures from %s: %v", dir, err)
	}
	if len(cases) == 0 {
		t.Fatalf("no block fixtures under %s — a layer that checks nothing must not report success", dir)
	}
	return cases
}
