package repoassess_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
	"github.com/PublicAI01/trajector-cli/internal/repoassess"
)

func sharedRepoAssessmentFixtures(t *testing.T) []conformance.RepoAssessmentCase {
	t.Helper()
	dir, tried := conformance.Find()
	if dir == "" {
		if os.Getenv(conformance.StrictEnv) == "1" {
			t.Fatalf("shared contract fixtures not found; looked in:\n  %s", strings.Join(tried, "\n  "))
		}
		t.Skipf("shared contract fixtures not found — this layer is not running.\nLooked in:\n  %s\nSet %s to point at them, or %s=1 to make this a failure.",
			strings.Join(tried, "\n  "), conformance.DirEnv, conformance.StrictEnv)
	}
	cases, err := conformance.LoadRepoAssessments(dir)
	if err != nil {
		t.Fatalf("loading repository assessment fixtures from %s: %v", dir, err)
	}
	if len(cases) == 0 {
		t.Fatalf("no repository assessment fixtures under %s — a layer that checks nothing must not report success", dir)
	}
	return cases
}

// reencode decodes a fixture's request into the client's submission and
// encodes it the way the client sends one.
func reencode(t *testing.T, request []byte) []byte {
	t.Helper()
	var s repoassess.Submission
	if err := json.Unmarshal(request, &s); err != nil {
		t.Fatalf("the request does not decode into a submission: %v", err)
	}
	p, err := repoassess.Encode(s)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return p.Body.Bytes()
}

// A submission the client could have sent is sent again byte for byte:
// the field order, the number formats, the null line counts, the cut
// message and the cut path list all survive the client's own encoding.
func TestSharedFixtures_ASubmissionTheClientCouldSendEncodesToTheSameBytes(t *testing.T) {
	ran := 0
	for _, c := range sharedRepoAssessmentFixtures(t) {
		if c.Request == nil || len(c.Meta.UnknownKeys) > 0 || len(c.Meta.OutOfBounds) > 0 {
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			if got := reencode(t, c.Request); !bytes.Equal(got, c.Request) {
				t.Errorf("encoded body differs from request.json:\n got %s\nwant %s", got, c.Request)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no fixture has a request the client could send")
	}
}

func TestSharedFixtures_TheByteExactCaseIsAmongThoseEncodedAgain(t *testing.T) {
	for _, c := range sharedRepoAssessmentFixtures(t) {
		if c.Meta.ByteExact && (c.Request == nil || len(c.Meta.UnknownKeys) > 0 || len(c.Meta.OutOfBounds) > 0) {
			t.Errorf("%s is byte_exact but is not a request the client could send", c.Name)
		}
	}
}

// pointerExists reports whether a JSON Pointer names a value in body.
func pointerExists(t *testing.T, body []byte, pointer string) bool {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	for tok := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		switch node := v.(type) {
		case map[string]any:
			next, ok := node[tok]
			if !ok {
				return false
			}
			v = next
		case []any:
			i := 0
			for _, r := range tok {
				i = i*10 + int(r-'0')
			}
			if i >= len(node) {
				return false
			}
			v = node[i]
		default:
			return false
		}
	}
	return true
}

func TestSharedFixtures_TheClientNeverSendsAKeyTheContractDoesNotHave(t *testing.T) {
	for _, c := range sharedRepoAssessmentFixtures(t) {
		if len(c.Meta.UnknownKeys) == 0 {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			got := reencode(t, c.Request)
			for _, p := range c.Meta.UnknownKeys {
				if !pointerExists(t, c.Request, p) {
					t.Fatalf("the fixture names %s, which its request does not hold", p)
				}
				if pointerExists(t, got, p) {
					t.Errorf("the encoded body still holds %s", p)
				}
			}
		})
	}
}

func TestSharedFixtures_TheClientNeverSendsAPathOutsideTheRepository(t *testing.T) {
	for _, c := range sharedRepoAssessmentFixtures(t) {
		if len(c.Meta.OutOfBounds) == 0 {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			bad, err := conformance.RepoAssessmentStringViolations(c.Request)
			if err != nil || len(bad) == 0 {
				t.Fatalf("the fixture's request breaks no string rule (%v): it proves nothing", err)
			}
			got := reencode(t, c.Request)
			if bad, err := conformance.RepoAssessmentStringViolations(got); err != nil || len(bad) > 0 {
				t.Errorf("the encoded body breaks the string rules: %v %v", bad, err)
			}
		})
	}
}

func TestSharedFixtures_EveryEncodedBodyKeepsTheStringRules(t *testing.T) {
	for _, c := range sharedRepoAssessmentFixtures(t) {
		if c.Request == nil {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			bad, err := conformance.RepoAssessmentStringViolations(reencode(t, c.Request))
			if err != nil || len(bad) > 0 {
				t.Errorf("violations %v, %v", bad, err)
			}
		})
	}
}
