package redact_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
	"github.com/PublicAI01/trajector-cli/internal/redact"
)

// shadowedSecret is random enough for the entropy layer and matches no
// format rule, so only a scan that sees it can mask it.
const shadowedSecret = "Zp3Hq8Ws2Kd6Nj0Vb7GmXk9mQ2vL8nR5tW"

// shadowedAWSKeyID is an AWS access key id, which the format layers mask
// wherever they see it.
const shadowedAWSKeyID = "AKIA" + "YRWQG5EJLPZLBYNP"

// A value whose key the same object names again is dropped by the JSON
// decoder, so the field-aware pass never sees it, while the raw bytes
// still carry it. Each document is checked on its own, which is the
// whole-document path, and as one line of two, which is the line path.
func TestJSONLBytes_MasksValueShadowedByDuplicateKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		doc    string
		secret string
	}{
		{"top level", `{"type":"user","content":"token ` + shadowedSecret + `","content":"hello"}`, shadowedSecret},
		{"aws key id", `{"a":"` + shadowedAWSKeyID + `","a":"x"}`, shadowedAWSKeyID},
		{"nested object", `{"m":{"text":"` + shadowedSecret + `","text":"ok"}}`, shadowedSecret},
		{"object in an array", `{"outer":[{"k":"` + shadowedSecret + `","k":"ok"}],"n":1}`, shadowedSecret},
		{"key spelled with an escape", `{"text":"` + shadowedSecret + `","t\u0065xt":"ok"}`, shadowedSecret},
		{"pretty printed", "{\n  \"text\": \"" + shadowedSecret + "\",\n  \"text\": \"ok\"\n}", shadowedSecret},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inputs := map[string]string{
				"as a document":      tt.doc,
				"as a line of JSONL": `{"type":"system","content":"ok"}` + "\n" + tt.doc + "\n",
			}
			for how, in := range inputs {
				if got := redactedString(t, in); strings.Contains(got, tt.secret) {
					t.Errorf("%s: %q went out unmasked: %s", how, tt.secret, got)
				}
			}
		})
	}
}

// Keys move from a list to a map once an object has more than a few;
// a key named again must be found on either side of that move.
func TestJSONLBytes_MasksValueShadowedByDuplicateKeyInAWideObject(t *testing.T) {
	t.Parallel()
	fields := []string{`"k0":"` + shadowedSecret + `"`}
	for i := 1; i < 25; i++ {
		fields = append(fields, fmt.Sprintf(`"k%d":"filler %d"`, i, i))
	}
	unique := "{" + strings.Join(fields, ",") + "}"
	if redact.HasDuplicateJSONKeys(unique) {
		t.Fatalf("%d distinct keys reported as a duplicate", len(fields))
	}
	for _, dup := range []string{"k0", "k20"} {
		doc := "{" + strings.Join(fields, ",") + `,"` + dup + `":"ok"}`
		if !redact.HasDuplicateJSONKeys(doc) {
			t.Errorf("%s named twice: not reported", dup)
		}
		if got := redactedString(t, doc); strings.Contains(got, shadowedSecret) {
			t.Errorf("%s named twice: %q went out unmasked: %s", dup, shadowedSecret, got)
		}
	}
}

// A document held in a string value is walked and then scanned as text
// in full, so a key it names twice hides nothing.
func TestJSONLBytes_MasksDuplicateKeyInsideAStringValue(t *testing.T) {
	t.Parallel()
	in := `{"content":"{\"a\":\"` + shadowedAWSKeyID + `\",\"a\":\"x\"}"}`
	if got := redactedString(t, in); strings.Contains(got, shadowedAWSKeyID) {
		t.Errorf("%q went out unmasked: %s", shadowedAWSKeyID, got)
	}
}

// Content that names no key twice must come out exactly as the
// field-aware pass alone makes it: the duplicate-key check decides
// nothing else. The corpus is this package's testdata and every JSON
// value the shared contract fixtures hold or generate. The block
// fixtures also mark the records that name a key twice, which the check
// must agree with.
func TestJSONLBytes_ContentWithoutDuplicateKeysTakesTheFieldAwarePass(t *testing.T) {
	t.Parallel()
	var units []string
	add := func(s string) { units = append(units, s) }

	files, err := filepath.Glob(filepath.Join("testdata", "*.json*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no testdata: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := redact.SpliceJSON(string(data)); err == nil {
			add(string(data)) // one JSON value: the whole-document path
			continue
		}
		assertLinesTakeTheFieldAwarePass(t, f, string(data))
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.TrimSpace(line) != "" {
				add(line)
			}
		}
	}

	dir := sharedFixtureDir(t)
	redaction, err := conformance.LoadRedaction(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range redaction {
		input, _ := c.Assemble()
		if c.Spelling == "json" {
			add(input)
		}
		add(asContentField(t, input))
	}
	batches, err := conformance.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range batches {
		for _, r := range append(c.Records, c.Restored...) {
			add(string(r))
		}
	}
	segments, err := conformance.LoadSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range segments {
		// The byte-cap case is 16 MiB: eight lines of one repeated pad
		// word, a shape the small case has too. Its size adds only
		// runtime here.
		if c.Source.FileBytes > 1<<20 {
			continue
		}
		lines, err := c.Source.Generate()
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		assertLinesTakeTheFieldAwarePass(t, c.Name, strings.Join(lines, ""))
	}
	blocks, err := conformance.LoadBlocks(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range blocks {
		for i, r := range c.Records {
			flagged := c.Meta.Records[i].JSONDuplicateKeys
			for _, b := range [][]byte{r.Input, r.Expect} {
				if got := redact.HasDuplicateJSONKeys(string(b)); got != flagged {
					t.Errorf("%s record %d: duplicate keys reported %v, fixture says %v", c.Name, i+1, got, flagged)
				}
				if !flagged {
					add(string(b))
				}
			}
		}
	}

	checked := 0
	for _, u := range units {
		want, err := redact.SpliceJSON(u)
		if err != nil {
			continue // not one JSON value: the raw-text path, untouched here
		}
		if redact.HasDuplicateJSONKeys(u) {
			t.Errorf("duplicate key reported in %.120s", u)
			continue
		}
		if got := redactedString(t, u); got != want {
			t.Errorf("output differs from the field-aware pass:\n got %.200s\nwant %.200s", got, want)
		}
		checked++
	}
	if checked < 100 {
		t.Fatalf("only %d of %d values checked", checked, len(units))
	}
	t.Logf("%d values checked", checked)
}

// assertLinesTakeTheFieldAwarePass checks the line path: each line of a
// JSONL file is the field-aware pass over that line alone.
func assertLinesTakeTheFieldAwarePass(t *testing.T, name, content string) {
	t.Helper()
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		spliced, err := redact.SpliceJSON(l)
		if err != nil {
			t.Fatalf("%s line %d: %v", name, i+1, err)
		}
		lines[i] = spliced
	}
	if got, want := redactedString(t, content), strings.Join(lines, "\n"); got != want {
		t.Errorf("%s: output differs from the field-aware pass line by line:\n got %.200s\nwant %.200s", name, got, want)
	}
}

func asContentField(t *testing.T, s string) string {
	t.Helper()
	doc, err := json.Marshal(map[string]string{"content": s})
	if err != nil {
		t.Fatal(err)
	}
	return string(doc)
}

func sharedFixtureDir(t *testing.T) string {
	t.Helper()
	dir, tried := conformance.Find()
	if dir == "" {
		if os.Getenv(conformance.StrictEnv) == "1" {
			t.Fatalf("shared contract fixtures not found; looked in:\n  %s", strings.Join(tried, "\n  "))
		}
		t.Skipf("shared contract fixtures not found; looked in:\n  %s\nSet %s to point at them, or %s=1 to make this a failure.",
			strings.Join(tried, "\n  "), conformance.DirEnv, conformance.StrictEnv)
	}
	return dir
}
