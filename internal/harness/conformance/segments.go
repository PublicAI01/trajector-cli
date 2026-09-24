package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// SegmentCase is one fixture of how a client cuts a file it reads into
// segments: a file, given as the rule that generates it, and the
// segments the contract requires of reading it from an empty cursor.
// The file is generated rather than stored because a case about a byte
// bound needs files of that size.
type SegmentCase struct {
	// Name is the fixture's directory name, used in test output.
	Name     string
	Meta     SegmentMeta
	Source   Source
	Segments []WantSegment
}

// SegmentMeta is the fixture's own statement of what it covers.
type SegmentMeta struct {
	Name            string `json:"name"`
	ContractRef     string `json:"contract_ref"`
	SegmentCapBytes int    `json:"segment_cap_bytes"`
	MaxLineBytes    int    `json:"max_line_bytes"`
}

// Source is the rule that generates a fixture's file, and the size and
// digest the generated file must have.
type Source struct {
	SessionID string `json:"session_id"`
	// File is the file's place in the session's directory: empty for
	// the session's main file.
	File         string       `json:"file"`
	LineTemplate string       `json:"line_template"`
	PadUnit      string       `json:"pad_unit"`
	Lines        []SourceLine `json:"lines"`
	FileBytes    int          `json:"file_bytes"`
	FileSHA256   string       `json:"file_sha256"`
}

// SourceLine is one line of a generated file: the id it carries and its
// length in bytes, line ending included.
type SourceLine struct {
	UUID  string `json:"uuid"`
	Bytes int    `json:"bytes"`
}

// WantSegment is one segment the contract requires: its index, its
// record id, and the source lines it holds, joined end to end.
type WantSegment struct {
	SegmentIndex int    `json:"segment_index"`
	RecordID     string `json:"record_id"`
	SourceLines  []int  `json:"source_lines"`
	Bytes        int    `json:"bytes"`
}

// Generate builds the file's lines by the fixture's rule: each line is
// the template with its id and session filled in and its padding cut
// from the pad unit repeated, to the exact length the line states. The
// whole file is checked against the size and digest the fixture states,
// so a generator that disagrees with the other sides fails here rather
// than in a comparison further on.
func (s Source) Generate() ([]string, error) {
	if s.PadUnit == "" {
		return nil, fmt.Errorf("source: empty pad unit")
	}
	lines := make([]string, len(s.Lines))
	digest := sha256.New()
	total := 0
	for i, l := range s.Lines {
		fixed := strings.NewReplacer("{uuid}", l.UUID, "{session_id}", s.SessionID).Replace(s.LineTemplate)
		pad := l.Bytes - (len(fixed) - len("{pad}"))
		if pad < 0 || !strings.Contains(fixed, "{pad}") {
			return nil, fmt.Errorf("source line %d: cannot pad to %d bytes", i, l.Bytes)
		}
		padding := strings.Repeat(s.PadUnit, pad/len(s.PadUnit)+1)[:pad]
		lines[i] = strings.Replace(fixed, "{pad}", padding, 1)
		if len(lines[i]) != l.Bytes {
			return nil, fmt.Errorf("source line %d: generated %d bytes, want %d", i, len(lines[i]), l.Bytes)
		}
		digest.Write([]byte(lines[i]))
		total += len(lines[i])
	}
	if total != s.FileBytes {
		return nil, fmt.Errorf("source: generated %d bytes, want %d", total, s.FileBytes)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != s.FileSHA256 {
		return nil, fmt.Errorf("source: generated file sha256 %s, want %s", got, s.FileSHA256)
	}
	return lines, nil
}

// LoadSegments reads every segment case under dir, sorted by name. A
// fixture set without the segments directory is an error: the caller
// found the fixtures, so a part of them that is missing is not the
// normal absence Find reports.
func LoadSegments(dir string) ([]SegmentCase, error) {
	root := filepath.Join(dir, "segments")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var cases []SegmentCase
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		base := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(base, "case.json")); err != nil {
			continue
		}
		c := SegmentCase{Name: e.Name()}
		if err := readJSON(filepath.Join(base, "case.json"), &c.Meta); err != nil {
			return nil, err
		}
		if err := readJSON(filepath.Join(base, "source.json"), &c.Source); err != nil {
			return nil, err
		}
		var want struct {
			Segments []WantSegment `json:"segments"`
		}
		if err := readJSON(filepath.Join(base, "segments.json"), &want); err != nil {
			return nil, err
		}
		c.Segments = want.Segments
		cases = append(cases, c)
	}
	slices.SortFunc(cases, func(a, b SegmentCase) int { return strings.Compare(a.Name, b.Name) })
	return cases, nil
}
