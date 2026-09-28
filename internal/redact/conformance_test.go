package redact_test

import (
	"os"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
)

func TestSharedRedactionFixturesMaskEveryMaskedSpanAndKeepEveryKeptOne(t *testing.T) {
	for _, c := range sharedRedactionFixtures(t) {
		t.Run(c.Group+"/"+c.ID, func(t *testing.T) {
			t.Parallel()
			input, spans := c.Assemble()
			outputs := map[string]string{"as a string value": redactedField(t, input)}
			if c.Spelling == "json" {
				outputs["as a document"] = redactedString(t, input)
			}
			for how, out := range outputs {
				for _, s := range spans {
					present := strings.Contains(out, s.Value)
					if s.Masked && present {
						t.Errorf("%s: bytes [%d,%d) should be masked by %s, got %q", how, s.Start, s.End, s.Rule, out)
					}
					if !s.Masked && !present {
						t.Errorf("%s: bytes [%d,%d) should be left alone, got %q", how, s.Start, s.End, out)
					}
				}
			}
		})
	}
}

func sharedRedactionFixtures(t *testing.T) []conformance.RedactionCase {
	t.Helper()
	dir, tried := conformance.Find()
	if dir == "" {
		if os.Getenv(conformance.StrictEnv) == "1" {
			t.Fatalf("shared contract fixtures not found; looked in:\n  %s", strings.Join(tried, "\n  "))
		}
		t.Skipf("shared contract fixtures not found — this layer is not running.\nLooked in:\n  %s\nSet %s to point at them, or %s=1 to make this a failure.",
			strings.Join(tried, "\n  "), conformance.DirEnv, conformance.StrictEnv)
	}
	cases, err := conformance.LoadRedaction(dir)
	if err != nil {
		t.Fatalf("loading redaction fixtures from %s: %v", dir, err)
	}
	if len(cases) == 0 {
		t.Fatalf("no redaction fixtures under %s — a layer that checks nothing must not report success", dir)
	}
	return cases
}
