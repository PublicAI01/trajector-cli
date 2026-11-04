package redact_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
	"github.com/PublicAI01/trajector-cli/internal/redact"
)

func TestRedactReadings_OneScanGivesTheReadingsOfTwoSeparateScans(t *testing.T) {
	t.Parallel()
	t.Run("testdata and generated content", func(t *testing.T) {
		t.Parallel()
		var c readingsCorpus
		files, err := filepath.Glob(filepath.Join("testdata", "*.json*"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no testdata: %v", err)
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			c.addContent(string(data))
		}
		c.addContent(string(generateBenchmarkJSONL(t, 1000)))
		c.addContent(string(generateBenchmarkRawText(1000)))
		c.addContent(string(generateBenchmarkEmbeddedJSONL(t, 1000)))
		c.addContent(string(generateBenchmarkReadResultJSONL(t, 26, 25)))
		c.check(t)
	})
	t.Run("shared contract fixtures", func(t *testing.T) {
		t.Parallel()
		dir := sharedFixtureDir(t)
		var c readingsCorpus
		redaction, err := conformance.LoadRedaction(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, rc := range redaction {
			input, _ := rc.Assemble()
			c.addContent(input)
		}
		batches, err := conformance.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, bc := range batches {
			for _, r := range append(bc.Records, bc.Restored...) {
				c.addContent(string(r))
			}
		}
		c.check(t)
	})
}

// readingsCorpus holds every text the field-aware walk or the raw-text
// scan can hand to a scan: a content that is one JSON value, each line,
// and each string value inside them, embedded documents included.
type readingsCorpus struct {
	texts map[string]struct{}
}

func (c *readingsCorpus) add(s string) {
	if c.texts == nil {
		c.texts = map[string]struct{}{}
	}
	c.texts[s] = struct{}{}
}

func (c *readingsCorpus) addContent(content string) {
	if json.Valid([]byte(content)) {
		c.add(content)
		c.addValue(content)
	}
	for line := range strings.SplitSeq(content, "\n") {
		if strings.TrimSpace(line) != "" {
			c.add(line)
			c.addValue(line)
		}
	}
}

// addValue adds every string value of s when s is one JSON value.
func (c *readingsCorpus) addValue(s string) {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return
	}
	var walk func(v any)
	walk = func(v any) {
		switch val := v.(type) {
		case map[string]any:
			for _, child := range val {
				walk(child)
			}
		case []any:
			for _, child := range val {
				walk(child)
			}
		case string:
			c.add(val)
			c.addValue(val)
		}
	}
	walk(v)
}

func (c *readingsCorpus) check(t *testing.T) {
	t.Helper()
	var masked, split int
	for s := range c.texts {
		gotRedacted, gotDeterministic := redact.ReadingsOfOneScan(s)
		wantRedacted, wantDeterministic := redact.ReadingsOfTwoScans(s)
		if gotRedacted != wantRedacted {
			t.Errorf("redacted reading differs:\n got %.200q\nwant %.200q", gotRedacted, wantRedacted)
		}
		if gotDeterministic != wantDeterministic {
			t.Errorf("deterministic reading differs:\n got %.200q\nwant %.200q", gotDeterministic, wantDeterministic)
		}
		if gotDeterministic != s {
			masked++
		}
		if gotRedacted != gotDeterministic {
			split++
		}
	}
	// A corpus where no layer 2-6 rule fires, or where layers 1 and 7
	// never add anything, cannot tell the two paths apart.
	if masked == 0 || split == 0 {
		t.Fatalf("corpus of %d texts: %d masked by layers 2-6, %d where layers 1 or 7 add regions; both must be non-zero", len(c.texts), masked, split)
	}
	t.Logf("%d texts: %d masked by layers 2-6, %d where layers 1 or 7 add regions", len(c.texts), masked, split)
}
