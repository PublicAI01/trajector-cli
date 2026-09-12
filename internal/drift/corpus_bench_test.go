package drift_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/redact"
)

const benchmarkLines = 5000

var benchmarkShapes = []string{
	"known_values.jsonl",
	"block_index_gap.jsonl",
	"free_text.jsonl",
	"agent_without_parent.jsonl",
}

func benchmarkCorpus(b *testing.B) string {
	b.Helper()
	var shapes []string
	for _, name := range benchmarkShapes {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			b.Fatal(err)
		}
		shapes = append(shapes, strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")...)
	}
	var corpus strings.Builder
	for i := 0; i < benchmarkLines; i++ {
		line := shapes[i%len(shapes)]
		line = strings.ReplaceAll(line, "msg_", fmt.Sprintf("msg%d_", i/3))
		line = strings.ReplaceAll(line, `"uuid":"`, fmt.Sprintf(`"uuid":"%d-`, i))
		corpus.WriteString(line)
		corpus.WriteByte('\n')
	}
	return corpus.String()
}

func benchmarkCapture() envelope.TranscriptCapture {
	return envelope.TranscriptCapture{
		ClientVersion: "0.1.0",
		Timestamp:     "2026-09-01T10:00:00Z",
		ProjectIDHash: strings.Repeat("a", 64),
		Injection:     envelope.InjectionProxy,
	}
}

func BenchmarkReadAndScanFiveThousandLines(b *testing.B) {
	corpus := benchmarkCorpus(b)
	dir := b.TempDir()
	path := filepath.Join(dir, "0f1e2d3c-4b5a-4968-8776-655443322110.jsonl")
	if err := os.WriteFile(path, []byte(corpus), 0o600); err != nil {
		b.Fatal(err)
	}
	capture := benchmarkCapture()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := follow.Read(follow.File{Path: path}, capture, follow.ReadOptions{Root: dir})
		if err != nil {
			b.Fatal(err)
		}
		if len(res.Segments) != 1 {
			b.Fatalf("segments = %d, want 1", len(res.Segments))
		}
		for _, seg := range res.Segments {
			if _, err := drift.Scan([]byte(seg.Lines)); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkRedactFiveThousandLines(b *testing.B) {
	seg := envelope.NewSegment("0f1e2d3c-4b5a-4968-8776-655443322110", "", 0, benchmarkCapture(), benchmarkCorpus(b))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := redact.RedactSegment(seg); err != nil {
			b.Fatal(err)
		}
	}
}
