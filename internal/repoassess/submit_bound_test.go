package repoassess_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/repoassess"
)

// newestFirst gives n commits in the order git log gives them, the
// newest first, each with a message made by message(i).
func newestFirst(n int, message func(i int) string) []repoassess.Commit {
	items := make([]repoassess.Commit, n)
	for i := range items {
		items[i] = repoassess.Commit{ID: fmt.Sprintf("%040x", n-i), Message: message(i)}
	}
	return items
}

// encodeWithin encodes items under the given bounds and checks what any
// body cut to its bounds must hold: it fits, the commits it keeps are
// the newest ones in their order, and truncated agrees with total.
func encodeWithin(t *testing.T, maxBody, maxGzip, total int, items []repoassess.Commit) repoassess.Commits {
	t.Helper()
	p, err := repoassess.EncodeWithin(repoassess.NewSubmission("2026-11-16", repoassess.Declaration{}, repoassess.Readout{},
		repoassess.Repo{}, repoassess.Commits{Total: total, Items: items}), maxBody, maxGzip)
	if err != nil {
		t.Fatalf("EncodeWithin: %v", err)
	}
	got := p.Submission.Commits
	if len(p.Body.Bytes()) > maxBody {
		t.Errorf("body = %d bytes, bound %d", len(p.Body.Bytes()), maxBody)
	}
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	zw.Write(p.Body.Bytes())
	zw.Close()
	if p.GzipBytes > maxGzip || zipped.Len() > maxGzip {
		t.Errorf("gzip = %d bytes (reported %d), bound %d", zipped.Len(), p.GzipBytes, maxGzip)
	}
	for i, c := range got.Items {
		if c.ID != items[i].ID {
			t.Fatalf("item %d = %s, want %s: a newer commit was left out before an older one", i, c.ID, items[i].ID)
		}
	}
	if got.Total != total || got.Truncated != (got.Total > len(got.Items)) {
		t.Errorf("total = %d, truncated %v, items %d", got.Total, got.Truncated, len(got.Items))
	}
	var sent struct {
		Commits struct {
			Total     int  `json:"total"`
			Truncated bool `json:"truncated"`
			Items     []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(p.Body.Bytes(), &sent); err != nil {
		t.Fatalf("body: %v", err)
	}
	if sent.Commits.Total != got.Total || sent.Commits.Truncated != got.Truncated || len(sent.Commits.Items) != len(got.Items) {
		t.Errorf("body commits = %d of %d (truncated %v), submission %d of %d (truncated %v)",
			len(sent.Commits.Items), sent.Commits.Total, sent.Commits.Truncated, len(got.Items), got.Total, got.Truncated)
	}
	return got
}

func TestEncode_LeavesOutTheOldestCommitsWhenTheBodyIsTooLarge(t *testing.T) {
	items := newestFirst(300, func(int) string { return strings.Repeat("a", 1000) })
	got := encodeWithin(t, 60_000, 1<<30, 300, items)
	if len(got.Items) == 0 || len(got.Items) >= len(items) || !got.Truncated {
		t.Errorf("kept %d of %d commits, truncated %v", len(got.Items), len(items), got.Truncated)
	}
}

func TestEncode_LeavesOutTheOldestCommitsWhenTheCompressedBodyIsTooLarge(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	items := newestFirst(300, func(int) string {
		var b strings.Builder
		for b.Len() < 1000 {
			for range 6 {
				b.WriteByte(byte('a' + r.IntN(26)))
			}
			b.WriteByte(' ')
		}
		return b.String()
	})
	got := encodeWithin(t, 1<<30, 40_000, 400, items)
	if len(got.Items) == 0 || len(got.Items) >= len(items) || !got.Truncated {
		t.Errorf("kept %d of %d commits, truncated %v", len(got.Items), len(items), got.Truncated)
	}
}

func TestEncode_KeepsEveryCommitWhenTheBodyFits(t *testing.T) {
	items := newestFirst(3, func(i int) string { return fmt.Sprint("commit ", i) })
	got := encodeWithin(t, 1<<20, 1<<20, 3, items)
	if len(got.Items) != 3 || got.Truncated {
		t.Errorf("kept %d of 3 commits, truncated %v", len(got.Items), got.Truncated)
	}
}

func TestEncode_FailsWhenTheReadoutAloneIsTooLarge(t *testing.T) {
	_, err := repoassess.EncodeWithin(repoassess.NewSubmission("2026-11-16", repoassess.Declaration{}, repoassess.Readout{},
		repoassess.Repo{}, repoassess.Commits{Total: 1, Items: newestFirst(1, func(int) string { return "m" })}), 64, 1<<20)
	if err == nil {
		t.Error("a body larger than its bound with no commits left was encoded")
	}
}
