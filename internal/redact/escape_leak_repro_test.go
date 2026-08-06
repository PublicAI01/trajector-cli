package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// Overtly fake: the FAKE segment marks it, the random tail keeps the
// entropy above the detection threshold.
const fakeHighEntropyKey = "sk-ant-api03-FAKE-Jq2XvB9dR4nT6kM1wZ8pL0cY5hG3fS7aQ2eU9iO4tW6rN8mK1jH5gD3bV7xC0zA"

// In an interpreted literal, "\\u2014" is the six characters
// backslash-u-2-0-1-4 — the JSON escape sequence itself. A literal em dash
// inside a raw backquoted string is a different byte sequence and exercises
// nothing about escapes.
var escDash, escHan, escHanUpper = "\\u2014", "\\u4e2d\\u6587", "\\u4E2D"

func TestEscapedUnicodeDoesNotDefeatRedaction(t *testing.T) {
	secret := fakeHighEntropyKey
	cases := []struct {
		name, raw string
		// Decoded text that must survive masking: replacing the secret may
		// normalise its own token's escaping, but must never drop content.
		wantIntact []string
	}{
		{"plain ASCII", `{"content":"key ` + secret + ` end"}`, []string{"key", "end"}},
		{"literal UTF-8", `{"content":"key ` + secret + ` 中文"}`, []string{"中文"}},
		{"escape in another field", `{"a":"` + escHan + `","content":"key ` + secret + `"}`, []string{"中文", "key"}},
		{"escaped dash, same string", `{"content":"key ` + secret + ` ` + escDash + ` end"}`, []string{"—", "end"}},
		{"escaped CJK, same string", `{"content":"key ` + secret + ` ` + escHan + `"}`, []string{"中文"}},
		{"escape before the secret", `{"content":"` + escHan + ` key ` + secret + `"}`, []string{"中文", "key"}},
		{"escaped slash, same string", `{"content":"key ` + secret + ` http:\/\/x"}`, []string{"http://x"}},
		{"upper-case hex escape", `{"content":"key ` + secret + ` ` + escHanUpper + `"}`, []string{"中"}},
		{"inside an array", `{"content":["key ` + secret + ` ` + escHan + `"]}`, []string{"中文"}},
		{"nested object", `{"a":{"content":"key ` + secret + ` ` + escDash + `"}}`, []string{"—"}},
	}
	for _, c := range cases {
		out, err := JSONLContent(c.raw)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if strings.Contains(out, secret) {
			t.Errorf("%s: credential left unmasked in %q", c.name, out)
		}
		var parsed any
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			t.Errorf("%s: output is not valid JSON: %v (%q)", c.name, err, out)
			continue
		}
		text := strings.Join(collectStrings(parsed, nil), "\n")
		for _, want := range c.wantIntact {
			if !strings.Contains(text, want) {
				t.Errorf("%s: masking lost %q: %q", c.name, want, out)
			}
		}
	}
}

func collectStrings(v any, acc []string) []string {
	switch t := v.(type) {
	case string:
		acc = append(acc, t)
	case []any:
		for _, e := range t {
			acc = collectStrings(e, acc)
		}
	case map[string]any:
		for _, e := range t {
			acc = collectStrings(e, acc)
		}
	}
	return acc
}

func TestRedactionPreservesEscapedRunes(t *testing.T) {
	raw := `{"content":"key ` + fakeHighEntropyKey + ` ` + escDash + ` ` + escHan + ` end","note":"` + escHanUpper + `"}`

	out, err := JSONLContent(raw)
	if err != nil {
		t.Fatalf("JSONLContent: %v", err)
	}
	var got struct {
		Content string `json:"content"`
		Note    string `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, out)
	}
	if strings.Contains(got.Content, fakeHighEntropyKey) {
		t.Fatalf("credential left unmasked: %q", got.Content)
	}
	for _, want := range []string{"—", "中文", "end"} {
		if !strings.Contains(got.Content, want) {
			t.Errorf("content lost %q: %q", want, got.Content)
		}
	}
	if got.Note != "中" {
		t.Errorf("untouched field was rewritten: note = %q, want 中", got.Note)
	}
}

func TestRedactionLeavesCleanRecordsByteIdentical(t *testing.T) {
	raw := `{"a":"中文","b":["x",  "y"],"c":{"d":"http:\/\/example.com"},"e":12,"f":"中"}`
	out, err := JSONLContent(raw)
	if err != nil {
		t.Fatalf("JSONLContent: %v", err)
	}
	if out != raw {
		t.Errorf("clean record was rewritten:\n got %q\nwant %q", out, raw)
	}
}
