package conformance

import (
	"path/filepath"
	"strings"
)

// RedactionCase is one fixture of the redaction rules both sides of the
// contract share: an input, assembled from parts, and the spans of it
// the rules must mask or must leave alone. The input is assembled rather
// than stored so that no fixture file holds a complete value shaped like
// a credential.
type RedactionCase struct {
	// Group is the fixture directory the case sits in; ID names the case
	// within it.
	Group string
	ID    string `json:"id"`
	// Spelling is "text" for an input read as one string value, or
	// "json" for an input that is a JSON document.
	Spelling string          `json:"spelling"`
	Parts    []RedactionPart `json:"parts"`
}

// RedactionPart is one piece of an input: plain text, or the fragments
// of a span that must be masked (with the rule that must find it) or
// must be kept.
type RedactionPart struct {
	Text string   `json:"text"`
	Mask []string `json:"mask"`
	Rule string   `json:"rule"`
	Keep []string `json:"keep"`
}

// Span is one stretch of an assembled input a fixture makes a claim
// about.
type Span struct {
	// Masked is true for a span that must be masked, false for one that
	// must be left alone.
	Masked bool
	// Start and End are byte offsets into the input.
	Start, End int
	Value      string
	// Rule names the rule that must find a masked span.
	Rule string
}

// Assemble joins the parts end to end, and the fragments of a span with
// nothing between them, returning the input and the spans it holds.
func (c RedactionCase) Assemble() (string, []Span) {
	var b strings.Builder
	var spans []Span
	for _, p := range c.Parts {
		frags, masked := p.Keep, false
		if p.Mask != nil {
			frags, masked = p.Mask, true
		}
		if frags == nil {
			b.WriteString(p.Text)
			continue
		}
		value := strings.Join(frags, "")
		spans = append(spans, Span{Masked: masked, Start: b.Len(), End: b.Len() + len(value), Value: value, Rule: p.Rule})
		b.WriteString(value)
	}
	return b.String(), spans
}

// LoadRedaction reads every redaction case under dir, group by group in
// name order and in file order within a group. A fixture set without the
// redaction directory is an error: the caller found the fixtures, so a
// part of them that is missing is not the normal absence Find reports.
func LoadRedaction(dir string) ([]RedactionCase, error) {
	root := filepath.Join(dir, "redaction")
	groups, err := caseDirs(root)
	if err != nil {
		return nil, err
	}
	var cases []RedactionCase
	for _, g := range groups {
		base := filepath.Join(root, g)
		var file struct {
			Cases []RedactionCase `json:"cases"`
		}
		if err := readJSON(filepath.Join(base, "cases.json"), &file); err != nil {
			return nil, err
		}
		for _, c := range file.Cases {
			c.Group = g
			cases = append(cases, c)
		}
	}
	return cases, nil
}
