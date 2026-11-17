package repoassess_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/repoassess"
)

// gitIn runs git in dir with env added and returns what it printed.
func gitIn(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// A repository built on disk goes through the whole path a submission
// takes, git log and its parsing included, and comes out as exactly
// the bytes the contract lays out.
func TestCollect_ARealRepositoryEncodesToTheContractBytes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on this machine")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, nil, "init", "-q", "--initial-branch=main")
	gitIn(t, dir, nil, "remote", "add", "origin", "git@github.com:someone/private-thing.git")
	write := func(name, content string) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lines := func(n int, word string) string { return strings.Repeat(word+"\n", n) }
	commit := func(email string, at int64, message string) string {
		env := []string{
			"GIT_AUTHOR_NAME=Someone", "GIT_AUTHOR_EMAIL=" + email, fmt.Sprintf("GIT_AUTHOR_DATE=@%d +0000", at),
			"GIT_COMMITTER_NAME=Someone", "GIT_COMMITTER_EMAIL=" + email, fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", at),
		}
		gitIn(t, dir, env, "add", "-A")
		gitIn(t, dir, env, "commit", "-q", "-m", message)
		return gitIn(t, dir, nil, "rev-parse", "HEAD")
	}

	write("package.json", lines(12, "{}"))
	write("README.md", lines(4, "readme"))
	a1 := commit("alice@corp.invalid", 1709340000, "Initial commit")
	write("assets/logo.png", "\x89PNG\x00\x01\x02")
	write("packages/@acme/ui/index.ts", lines(5, "export {}"))
	b2 := commit("bob@corp.invalid", 1714000000, "Add logo and export the button from the ui package")
	write("packages/@acme/ui/index.ts", lines(4, "export {}")+lines(2, "export * from './button'"))
	write("src/sync/client.test.ts", lines(30, "test()"))
	c3 := commit("alice@corp.invalid", 1718000000, "Add retry with backoff to the sync client\n\nRetries three times before giving up.\n\n"+
		"Co-authored-by: Claude <noreply@anthropic.com>\nSigned-off-by: Alice <alice@corp.invalid>")

	repo, commits, err := repoassess.Collect(t.Context(), dir)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	p, err := repoassess.Encode(repoassess.NewSubmission("2026-11-16", repoassess.Declaration{
		Ownership: "sole_author", EverPublic: "never", InProduction: "real_users", AIShare: "5_to_30",
	}, repoassess.Readout{}, repo, commits))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `{"format":1,"consent_version":"2026-11-16",` +
		`"repo":{"root_commit":"` + a1 + `","head_commit":"` + c3 + `","remote_hosts":["github"]},` +
		`"declaration":{"ownership":"sole_author","ever_public":"never","in_production":"real_users","ai_share":"5_to_30"},` +
		`"readout":{"rule_version":0,"as_of":"0001-01-01T00:00:00Z","shallow":false,"commits":0,"first_commit":"","span_years":0,"active_months":0,` +
		`"authors":0,"authors_ge3":0,"top_author_share":0,"commits_by_year":[],"commits_last3y":0,"test_commits":0,"src_commits":0,"cochange":0,` +
		`"cochange_src1_5":0,"cochange_src2p":0,"cochange_last3y":0,"adjacent_pairs":0,"cochange_or_adj":0,"cochange_rate":0,"ci_commits":0,` +
		`"agent_commit_share":0,"files":0,"src_files":0,"test_files":0,"test_code_files":0,"test_file_share":0,"main_lang":"","main_lang_share":0,` +
		`"n_langs_10pct":0,"has_ci":false,"has_manifest":false,"has_lock":false,"manifest_lock_paired":false,"has_docker":false},` +
		`"commits":{"total":3,"truncated":false,"items":[` +
		`{"id":"` + c3 + `","authored_at":1718000000,"author":0,"agent":true,` +
		`"message":"Add retry with backoff to the sync client\n\nRetries three times before giving up.","message_truncated":false,` +
		`"paths":[{"path":"packages/@acme/ui/index.ts","added":2,"deleted":1},{"path":"src/sync/client.test.ts","added":30,"deleted":0}],` +
		`"path_count":2,"paths_truncated":false},` +
		`{"id":"` + b2 + `","authored_at":1714000000,"author":1,"agent":false,` +
		`"message":"Add logo and export the button from the ui package","message_truncated":false,` +
		`"paths":[{"path":"assets/logo.png","added":null,"deleted":null},{"path":"packages/@acme/ui/index.ts","added":5,"deleted":0}],` +
		`"path_count":2,"paths_truncated":false},` +
		`{"id":"` + a1 + `","authored_at":1709340000,"author":0,"agent":false,"message":"Initial commit","message_truncated":false,` +
		`"paths":[{"path":"README.md","added":4,"deleted":0},{"path":"package.json","added":12,"deleted":0}],` +
		`"path_count":2,"paths_truncated":false}]}}`
	if got := p.Body.Bytes(); !bytes.Equal(got, []byte(want)) {
		t.Errorf("body differs:\n got %s\nwant %s", got, want)
	}
}
