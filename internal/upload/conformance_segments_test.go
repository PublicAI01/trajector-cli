package upload_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// TestSegmentFixturesMatchHowThisClientCutsAFile reads each shared
// segment fixture's file the way this client reads a session file —
// from an empty cursor to its end — and holds the segments it makes
// against the ones the fixture requires: their indexes, record ids,
// and lines. The segments are then uploaded, and what the fake service
// receives must carry the same lines byte for byte: the fixture's lines
// hold nothing that redaction changes.
func TestSegmentFixturesMatchHowThisClientCutsAFile(t *testing.T) {
	for _, c := range sharedSegmentFixtures(t) {
		t.Run(c.Name, func(t *testing.T) {
			lines, err := c.Source.Generate()
			if err != nil {
				t.Fatal(err)
			}
			path := sessionFilePath(t, c.Source)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
				t.Fatal(err)
			}

			f := newFixture(t)
			const projectIDHash = "hash-p1"
			registry := follow.Open(filepath.Join(t.TempDir(), "follow"))
			if err := registry.Register(projectIDHash, path); err != nil {
				t.Fatal(err)
			}
			var made []envelope.Segment
			reader := follow.Reader{
				Registry:      registry,
				ProjectIDHash: projectIDHash,
				Options:       follow.ReadOptions{Root: filepath.Dir(path)},
				Capture:       recordCapture(time.Now(), projectIDHash),
				Store: func(res follow.ReadResult) follow.Storing {
					for _, seg := range res.Segments {
						if err := f.spool.WriteSegment(seg); err != nil {
							t.Error(err)
							return follow.NotStored
						}
					}
					made = append(made, res.Segments...)
					return follow.Stored
				},
			}

			reader.Advance(path)
			assertFixtureSegments(t, c, lines, made)
			before := len(made)
			reader.Advance(path)
			if len(made) != before {
				t.Errorf("a second read made %d more segments, want none", len(made)-before)
			}

			f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
			res, err := f.uploader.Flush(true)
			if err != nil || res.Outcome != upload.Uploaded {
				t.Fatalf("flush = %+v, %v, want every segment uploaded", res, err)
			}
			uploaded := uploadedSegmentLines(t, f)
			for _, seg := range made[:before] {
				if uploaded[seg.RecordID] != seg.Lines {
					t.Errorf("segment %d uploaded with %d bytes of lines, want the %d bytes read unchanged", seg.SegmentIndex, len(uploaded[seg.RecordID]), len(seg.Lines))
				}
			}
			if len(uploaded) != before {
				t.Errorf("service received %d segments, want %d", len(uploaded), before)
			}
		})
	}
}

func assertFixtureSegments(t *testing.T, c conformance.SegmentCase, lines []string, made []envelope.Segment) {
	t.Helper()
	if len(made) != len(c.Segments) {
		t.Fatalf("reading the file made %d segments, the fixture requires %d", len(made), len(c.Segments))
	}
	var joined strings.Builder
	for i, want := range c.Segments {
		got := made[i]
		if got.SegmentIndex != want.SegmentIndex {
			t.Errorf("segment %d has index %d, want %d", i, got.SegmentIndex, want.SegmentIndex)
		}
		if got.RecordID != want.RecordID || envelope.SegmentRecordID(c.Source.SessionID, c.Source.File, want.SegmentIndex) != want.RecordID {
			t.Errorf("segment %d has record id %s, want %s", i, got.RecordID, want.RecordID)
		}
		var wantLines strings.Builder
		for _, n := range want.SourceLines {
			wantLines.WriteString(lines[n])
		}
		if got.Lines != wantLines.String() || len(got.Lines) != want.Bytes {
			t.Errorf("segment %d holds %d bytes, want source lines %v, %d bytes", i, len(got.Lines), want.SourceLines, want.Bytes)
		}
		joined.WriteString(got.Lines)
	}
	if joined.String() != strings.Join(lines, "") {
		t.Errorf("segments joined hold %d bytes, want the %d bytes of the file", joined.Len(), c.Source.FileBytes)
	}
}

// sessionFilePath is where a fixture's file sits in a session
// directory: the session's main file, or a file under the session's
// own directory.
func sessionFilePath(t *testing.T, s conformance.Source) string {
	t.Helper()
	dir := t.TempDir()
	if s.File == "" {
		return filepath.Join(dir, s.SessionID+".jsonl")
	}
	return filepath.Join(dir, s.SessionID, filepath.FromSlash(s.File))
}

// uploadedSegmentLines is the lines of every segment the fake service
// received, by record id.
func uploadedSegmentLines(t *testing.T, f *fixture) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, r := range f.server.Requests() {
		ix := uploadedIndex(t, r)
		stream := uploadedStream(t, r)
		for _, item := range ix.Records {
			body := stream[item.Offset : item.Offset+item.Size]
			kind, err := envelope.KindOf(body)
			if err != nil {
				t.Fatal(err)
			}
			if kind != envelope.KindSegment {
				continue
			}
			seg, err := envelope.ParseSegment(body)
			if err != nil {
				t.Fatal(err)
			}
			got[seg.RecordID] = seg.Lines
		}
	}
	return got
}

func sharedSegmentFixtures(t *testing.T) []conformance.SegmentCase {
	t.Helper()
	dir, tried := conformance.Find()
	if dir == "" {
		if os.Getenv(conformance.StrictEnv) == "1" {
			t.Fatalf("shared contract fixtures not found; looked in:\n  %s", join(tried))
		}
		t.Skipf("shared contract fixtures not found — this layer is not running.\nLooked in:\n  %s\nSet %s to point at them, or %s=1 to make this a failure.",
			join(tried), conformance.DirEnv, conformance.StrictEnv)
	}
	cases, err := conformance.LoadSegments(dir)
	if err != nil {
		t.Fatalf("loading segment fixtures from %s: %v", dir, err)
	}
	if len(cases) == 0 {
		t.Fatalf("no segment fixtures under %s — a layer that checks nothing must not report success", dir)
	}
	return cases
}
