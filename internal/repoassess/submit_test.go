package repoassess_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
	"github.com/PublicAI01/trajector-cli/internal/repoassess"
)

func encodeCommits(t *testing.T, total int, items ...repoassess.Commit) repoassess.Prepared {
	t.Helper()
	p, err := repoassess.Encode(repoassess.NewSubmission("2026-11-16", repoassess.Declaration{}, repoassess.Readout{},
		repoassess.Repo{}, repoassess.Commits{Total: total, Items: items}))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return p
}

func paths(names ...string) []repoassess.ChangedPath {
	out := make([]repoassess.ChangedPath, len(names))
	for i, n := range names {
		out[i] = repoassess.ChangedPath{Path: n}
	}
	return out
}

func TestEncode_CutsALongMessageOnACharacterBoundary(t *testing.T) {
	long := strings.Repeat("提交", 400) // 2,400 bytes
	got := encodeCommits(t, 1, repoassess.Commit{Message: long}).Submission.Commits.Items[0]
	if !got.MessageTruncated || len(got.Message) > 2000 || len(got.Message) < 1998 || !utf8.ValidString(got.Message) {
		t.Errorf("message = %d bytes, valid %v, truncated %v", len(got.Message), utf8.ValidString(got.Message), got.MessageTruncated)
	}
	short := encodeCommits(t, 1, repoassess.Commit{Message: "Fix the parser"}).Submission.Commits.Items[0]
	if short.MessageTruncated || short.Message != "Fix the parser" {
		t.Errorf("short message = %q, truncated %v", short.Message, short.MessageTruncated)
	}
}

func TestEncode_KeepsTheFirstPathsAndCountsAll(t *testing.T) {
	var many []string
	for i := range 201 {
		many = append(many, fmt.Sprintf("src/part_%03d.go", i))
	}
	got := encodeCommits(t, 1, repoassess.Commit{Paths: paths(many...)}).Submission.Commits.Items[0]
	if len(got.Paths) != 200 || got.PathCount != 201 || !got.PathsTruncated || got.Paths[199].Path != "src/part_199.go" {
		t.Errorf("paths = %d, count %d, truncated %v", len(got.Paths), got.PathCount, got.PathsTruncated)
	}
}

func TestEncode_LeavesOutAPathTooLongOrOutsideTheRepository(t *testing.T) {
	tests := []string{
		strings.Repeat("a", 1025),
		"/etc/passwd",
		`\share\x.go`,
		"C:/x.go",
		"a/../../b.go",
		"http://x/y",
	}
	for _, bad := range tests {
		got := encodeCommits(t, 1, repoassess.Commit{Paths: paths("ok.go", bad)}).Submission.Commits.Items[0]
		if len(got.Paths) != 1 || got.Paths[0].Path != "ok.go" || got.PathCount != 2 || !got.PathsTruncated {
			t.Errorf("%.20q: paths %+v, count %d, truncated %v", bad, got.Paths, got.PathCount, got.PathsTruncated)
		}
	}
	kept := encodeCommits(t, 1, repoassess.Commit{Paths: paths("packages/@scope/pkg/index.ts", "a..b/c.go")}).Submission.Commits.Items[0]
	if len(kept.Paths) != 2 || kept.PathsTruncated {
		t.Errorf("relative paths with @ and dots were left out: %+v", kept.Paths)
	}
}

func TestEncode_KeepsTheNewestCommitsUpToTheBound(t *testing.T) {
	items := make([]repoassess.Commit, 20001)
	for i := range items {
		items[i] = repoassess.Commit{ID: fmt.Sprintf("%040x", i), Message: "m"}
	}
	got := encodeCommits(t, 25000, items...).Submission.Commits
	if len(got.Items) != 20000 || got.Items[0].ID != items[0].ID || got.Total != 25000 || !got.Truncated {
		t.Errorf("items = %d, first %s, total %d, truncated %v", len(got.Items), got.Items[0].ID, got.Total, got.Truncated)
	}
	all := encodeCommits(t, 2, repoassess.Commit{Message: "a"}, repoassess.Commit{Message: "b"}).Submission.Commits
	if all.Truncated {
		t.Error("a complete list is marked truncated")
	}
}

func TestEncode_MasksMessagesAndPaths(t *testing.T) {
	p := encodeCommits(t, 1, repoassess.Commit{
		Message: "Rotate the key\n\nIt was sent to dev.person@example.com by mistake.",
		Paths:   paths("notes/dev.person@example.com.md"),
	})
	if strings.Contains(string(p.Body.Bytes()), "dev.person@example.com") {
		t.Errorf("an email address was sent: %s", p.Body.Bytes())
	}
	if bad, err := conformance.RepoAssessmentStringViolations(p.Body.Bytes()); err != nil || len(bad) > 0 {
		t.Errorf("string rules: %v %v", bad, err)
	}
}

func TestEncode_WritesEmptyListsAsListsAndNoHTMLEscapes(t *testing.T) {
	body := string(encodeCommits(t, 1, repoassess.Commit{Message: "a <b> & c", Paths: []repoassess.ChangedPath{}}).Body.Bytes())
	for _, want := range []string{`"remote_hosts":[]`, `"paths":[]`, `"message":"a <b> & c"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s: %s", want, body)
		}
	}
	none := string(encodeCommits(t, 0).Body.Bytes())
	if !strings.Contains(none, `"items":[]`) {
		t.Errorf("body lacks an empty item list: %s", none)
	}
}

func TestDeclaration_AsksOwnershipFirstAndNeverSendsAnAnswerThatStops(t *testing.T) {
	if repoassess.Questions[0].Key != "ownership" {
		t.Fatalf("the first question is %q", repoassess.Questions[0].Key)
	}
	answers := map[string]string{"ownership": "sole_author", "ever_public": "never", "in_production": "real_users", "ai_share": "5_to_30"}
	d, err := repoassess.NewDeclaration(answers)
	if err != nil || d != (repoassess.Declaration{Ownership: "sole_author", EverPublic: "never", InProduction: "real_users", AIShare: "5_to_30"}) {
		t.Fatalf("NewDeclaration = %+v, %v", d, err)
	}
	for _, o := range repoassess.Questions[0].Options {
		if !o.Stops {
			continue
		}
		answers["ownership"] = o.Value
		if _, err := repoassess.NewDeclaration(answers); err == nil {
			t.Errorf("the answer %q, which stops, made a declaration", o.Value)
		}
	}
	delete(answers, "ai_share")
	answers["ownership"] = "sole_author"
	if _, err := repoassess.NewDeclaration(answers); err == nil {
		t.Error("a missing answer made a declaration")
	}
}

func TestEncode_MasksASecretThatCrossesTheMessageBoundBeforeCuttingIt(t *testing.T) {
	secret := "AKIA" + "YRWQG5EJLPZLBYNP"
	message := strings.Repeat("a", 1989) + " " + secret + " and more text after it"
	if start := strings.Index(message, secret); start >= 2000 || start+len(secret) <= 2000 {
		t.Fatalf("the secret spans bytes %d to %d: it does not cross the bound", start, start+len(secret))
	}
	got := encodeCommits(t, 1, repoassess.Commit{Message: message}).Submission.Commits.Items[0]
	if !strings.Contains(got.Message, "REDACTED") || strings.Contains(got.Message, secret[4:12]) || len(got.Message) > 2000 {
		t.Errorf("message ends %q (%d bytes); want REDACTED in place of the secret, and no part of it", got.Message[1980:], len(got.Message))
	}
}

func TestEncode_DropsEveryMessageLineThatNamesAPerson(t *testing.T) {
	tests := []struct{ message, want string }{
		{"Fix the parser\n\nCo-authored-by: Ann <ann@corp.invalid>\nSigned-off-by: Bo <bo@corp.invalid>", "Fix the parser"},
		{"Fix\n\nreported-by: Cy\n  Acked-By:Di\ncc: Ed <ed@corp.invalid>\nTested-by : Fay\n", "Fix"},
		{"Fix\r\n\r\nSigned-off-by: Gus\r\n", "Fix"},
		{"Fix the by: line handling\n\nThe bylaws say Cc: is fine mid-line.", "Fix the by: line handling\n\nThe bylaws say Cc: is fine mid-line."},
		{"Signed-off-by: Hal", ""},
	}
	for _, tt := range tests {
		got := encodeCommits(t, 1, repoassess.Commit{Message: tt.message}).Submission.Commits.Items[0].Message
		if got != tt.want {
			t.Errorf("%q became %q, want %q", tt.message, got, tt.want)
		}
	}
}
