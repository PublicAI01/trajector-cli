package cli_test

import (
	"bytes"
	"cmp"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/conformance"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/repoassess"
)

// allAnswers answers the four questions on stdin, in order, by number.
const allAnswers = "1\n1\n2\n2\n"

// answerFlags answer the four questions without a prompt.
var answerFlags = []string{"--ownership=sole_author", "--ever-public=never", "--in-production=real_users", "--ai-share=5_to_30"}

// submitRepo is a repository with two authors and a commit that
// changes a test and its implementation together.
func submitRepo(t *testing.T) *assessRepo {
	t.Helper()
	r := newAssessRepo(t)
	r.commit(change{files: []string{"calc.go", "README.md"}, day: 1, message: "Add the calculator"})
	r.commit(change{files: []string{"calc.go", "calc_test.go"}, day: 2, author: "bob@example.invalid", message: "Fix rounding\n\nThe last digit was dropped."})
	return r
}

func (r *assessRepo) rev(name string) string {
	r.t.Helper()
	out, err := exec.CommandContext(r.t.Context(), "git", "-C", r.dir, "rev-parse", name).Output()
	if err != nil {
		r.t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func submitEnv(t *testing.T) (*clitest.Env, string) {
	t.Helper()
	e := assessEnv(t)
	e.Paired()
	return e, filepath.Join(t.TempDir(), "preview.json")
}

func submitArgs(dir, preview string, extra ...string) []string {
	return append([]string{"repo", "assess", dir, "--submit", "--preview=" + preview}, extra...)
}

// sentBody is the one submission the fake service received, decompressed.
func sentBody(t *testing.T, e *clitest.Env) []byte {
	t.Helper()
	got := e.Service().RepoAssessments()
	if len(got) != 1 {
		t.Fatalf("%d submissions received, want 1", len(got))
	}
	body, err := fakeplatform.RepoAssessmentBody(got[0])
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertNothingSent(t *testing.T, e *clitest.Env) {
	t.Helper()
	if got := e.Service().Requests(); len(got) != 0 {
		t.Errorf("%d requests reached the service, want none", len(got))
	}
}

func TestRepoSubmit_SendsWhatThePreviewShowsOnceTheUserConfirms(t *testing.T) {
	r := submitRepo(t)
	e, preview := submitEnv(t)
	e.Service().StoresRepoAssessment("ra-1")

	got := e.RunInput(allAnswers+"yes\nyes\n", submitArgs(r.dir, preview)...)
	if got.Exit != 0 {
		t.Fatalf("exit = %d, stderr %q\nstdout %s", got.Exit, got.Stderr, got.Stdout)
	}
	for _, want := range []string{"Who owns this repository?", consent.RepoTermsText, "Submission id: ra-1", "support@publicai.io"} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	body := sentBody(t, e)
	shown, err := os.ReadFile(preview)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, shown); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compact.Bytes(), body) {
		t.Errorf("what was sent differs from the preview:\n sent %s\nshown %s", body, compact.Bytes())
	}

	var s repoassess.Submission
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	if s.Format != 1 || s.ConsentVersion != consent.RepoTermsVersion {
		t.Errorf("format %d, consent_version %q", s.Format, s.ConsentVersion)
	}
	if s.Declaration != (repoassess.Declaration{Ownership: "sole_author", EverPublic: "never", InProduction: "real_users", AIShare: "5_to_30"}) {
		t.Errorf("declaration = %+v", s.Declaration)
	}
	if s.Repo.HeadCommit != r.rev("HEAD") || s.Repo.RootCommit != r.rev("HEAD~1") || len(s.Repo.RemoteHosts) != 0 {
		t.Errorf("repo = %+v", s.Repo)
	}
	if s.Readout.Commits != 2 || s.Readout.Cochange != 1 {
		t.Errorf("readout = %+v", s.Readout)
	}
	items := s.Commits.Items
	if s.Commits.Total != 2 || s.Commits.Truncated || len(items) != 2 {
		t.Fatalf("commits = %+v", s.Commits)
	}
	newest, oldest := items[0], items[1]
	if newest.ID != r.rev("HEAD") || newest.Message != "Fix rounding\n\nThe last digit was dropped." || newest.Author != 1 || oldest.Author != 0 {
		t.Errorf("commits = %+v", items)
	}
	var names []string
	for _, p := range newest.Paths {
		names = append(names, p.Path)
		if p.Added == nil || *p.Added != 1 || p.Deleted == nil || *p.Deleted != 0 {
			t.Errorf("%s: added %v, deleted %v; want 1 and 0", p.Path, p.Added, p.Deleted)
		}
	}
	if !slices.Equal(names, []string{"calc.go", "calc_test.go"}) || newest.PathCount != 2 || newest.PathsTruncated {
		t.Errorf("paths = %v, count %d", names, newest.PathCount)
	}
	for _, leak := range []string{"alice", "bob", "example.invalid", r.dir, e.Home()} {
		if bytes.Contains(body, []byte(leak)) {
			t.Errorf("the submission holds %q", leak)
		}
	}
	if bad, err := conformance.RepoAssessmentStringViolations(body); err != nil || len(bad) > 0 {
		t.Errorf("string rules: %v %v", bad, err)
	}
}

func TestRepoSubmit_AsksNothingOnceTheTermsAreAgreedAndEveryAnswerIsGiven(t *testing.T) {
	r := submitRepo(t)
	e, preview := submitEnv(t)
	e.Service().StoresRepoAssessment("ra-2")
	if got := e.RunInput(allAnswers+"yes\nno\n", submitArgs(r.dir, preview)...); got.Exit != 1 {
		t.Fatalf("declining to send: exit = %d, stderr %q", got.Exit, got.Stderr)
	}
	assertNothingSent(t, e)

	got := e.Run(submitArgs(r.dir, preview+".2", append(answerFlags, "--yes")...)...)
	if got.Exit != 0 {
		t.Fatalf("exit = %d, stderr %q", got.Exit, got.Stderr)
	}
	if strings.Contains(got.Stdout, consent.RepoTermsText) || strings.Contains(got.Stdout, "Answer 1-") {
		t.Errorf("a run with every answer given still asked:\n%s", got.Stdout)
	}
	if !strings.Contains(got.Stdout, "You agreed to the repository assessment terms, version "+consent.RepoTermsVersion) {
		t.Errorf("stdout does not name the terms in force:\n%s", got.Stdout)
	}
	sentBody(t, e)
}

func TestRepoSubmit_SendsNothing(t *testing.T) {
	tests := []struct {
		name       string
		signedOut  bool
		shallow    bool
		input      string
		extra      []string
		wantOut    string
		wantErr    string
		wantAsked  []string
		notAsked   []string
		noPreview  bool
		termsAfter bool
	}{
		{
			name: "when the device is not signed in", signedOut: true, input: allAnswers + "yes\nyes\n",
			wantErr: "trajector login", notAsked: []string{"Who owns this repository?"}, noPreview: true,
		},
		{
			name: "when the repository is a shallow clone", shallow: true, input: allAnswers + "yes\nyes\n",
			wantOut: "This clone is shallow", wantErr: "git fetch --unshallow", notAsked: []string{"Who owns this repository?"}, noPreview: true,
		},
		{
			name: "when the user may not share the repository", input: "4\n",
			wantOut: "Nothing was sent", wantAsked: []string{"Who owns this repository?"}, notAsked: []string{"Was this code ever public?"}, noPreview: true,
		},
		{
			name: "when the user is not sure who owns it", extra: []string{"--ownership=not_sure"},
			wantOut: "Nothing was sent", notAsked: []string{"Who owns this repository?", "Was this code ever public?"}, noPreview: true,
		},
		{
			name: "when the user does not agree to the terms", input: allAnswers + "no\n",
			wantOut: "Terms not agreed to. Nothing was sent. The preview stays in", wantAsked: []string{"Do you agree to these terms?"},
		},
		{
			name: "when the user does not confirm", input: allAnswers + "yes\nno\n",
			wantOut: "Nothing was sent. The preview stays in", termsAfter: true,
		},
		{
			name: "when an answer is missing and nobody is there to ask", extra: answerFlags[:3],
			wantErr: "--ai-share=<answer>", noPreview: true,
		},
		{
			name: "when --yes is given before the terms were agreed to", extra: append(answerFlags, "--yes"),
			wantErr: "run this command once without --yes to read them; nothing was sent, and the preview stays in",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := submitRepo(t)
			e, preview := submitEnv(t)
			if tt.signedOut {
				e = assessEnv(t)
			}
			dir := r.dir
			if tt.shallow {
				dir = filepath.Join(t.TempDir(), "assessed-shallow")
				r.git(nil, "clone", "-q", "--depth", "1", "file://"+filepath.ToSlash(r.dir), dir)
			}
			got := e.RunInput(tt.input, submitArgs(dir, preview, tt.extra...)...)
			if got.Exit != 1 {
				t.Errorf("exit = %d, want 1; stderr %q", got.Exit, got.Stderr)
			}
			if !strings.Contains(got.Stdout, tt.wantOut) || !strings.Contains(got.Stderr, tt.wantErr) {
				t.Errorf("stdout %q\nstderr %q\nwant %q and %q", got.Stdout, got.Stderr, tt.wantOut, tt.wantErr)
			}
			for _, q := range tt.wantAsked {
				if !strings.Contains(got.Stdout, q) {
					t.Errorf("%q was not asked", q)
				}
			}
			for _, q := range tt.notAsked {
				if strings.Contains(got.Stdout, q) {
					t.Errorf("%q was asked", q)
				}
			}
			if _, err := os.Stat(preview); tt.noPreview != os.IsNotExist(err) {
				t.Errorf("preview written = %v, want %v", err == nil, !tt.noPreview)
			}
			assertNothingSent(t, e)
			if !tt.signedOut {
				again := e.Run(submitArgs(r.dir, preview+".again", append(answerFlags, "--yes")...)...)
				if agreed := strings.Contains(again.Stdout, "You agreed to the repository assessment terms"); agreed != tt.termsAfter {
					t.Errorf("a later run finds the terms agreed = %v, want %v; stderr %q", agreed, tt.termsAfter, again.Stderr)
				}
			}
		})
	}
}

func TestRepoSubmit_SaysWhatTheServiceAnswered(t *testing.T) {
	retry := fakeplatform.JSON(429, map[string]string{"error": "rate_limited"})
	retry.Header.Set("Retry-After", "30")
	tests := []struct {
		name string
		resp fakeplatform.Response
		want string
	}{
		{"401", fakeplatform.JSON(401, map[string]string{"error": "unauthorized"}), "run `trajector login`"},
		{"426", fakeplatform.Refuses426("9.9.9", ""), "version 9.9.9 or later; run `trajector upgrade`"},
		{"400 terms version", fakeplatform.JSON(400, map[string]string{"error": "consent_version_unsupported"}), "no longer takes submissions under the terms"},
		{"400 invalid", fakeplatform.JSON(400, map[string]string{"error": "invalid_request"}), "(invalid_request)"},
		{"404", fakeplatform.JSON(404, map[string]string{"error": "not_found"}), "does not take repository assessments now"},
		{"413", fakeplatform.JSON(413, map[string]string{"error": "payload_too_large"}), "larger than the service takes"},
		{"429", retry, "try again later, in 30s or more"},
		{"500", fakeplatform.JSON(500, map[string]string{"error": "internal_error"}), "status 500 (internal_error)"},
		{"502 with no error code", fakeplatform.JSON(502, map[string]string{"error": "Bad gateway: upstream down"}), "status 502."},
		{"200 without an id", fakeplatform.JSON(200, map[string]string{}), "status 200 but no submission id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := submitRepo(t)
			e, preview := submitEnv(t)
			e.Service().AnswersRepoAssessment(tt.resp)
			got := e.RunInput(allAnswers+"yes\nyes\n", submitArgs(r.dir, preview)...)
			if got.Exit != 1 || !strings.Contains(got.Stderr, tt.want) || !strings.Contains(got.Stderr, "Nothing is kept on this machine to retry it") {
				t.Errorf("exit = %d, stderr %q; want 1 and %q", got.Exit, got.Stderr, tt.want)
			}
			if strings.Contains(got.Stdout, "Submission id") {
				t.Errorf("a failure printed a submission id:\n%s", got.Stdout)
			}
			if phrase := http.StatusText(cmp.Or(tt.resp.Status, http.StatusOK)); strings.Contains(got.Stderr, phrase) || strings.Contains(got.Stderr, "upstream") {
				t.Errorf("stderr %q repeats the service's own text", got.Stderr)
			}
			if n := len(e.Service().RepoAssessments()); n != 1 {
				t.Errorf("%d submissions sent, want exactly 1: nothing retries", n)
			}
		})
	}
}

func TestRepoSubmit_StatesOnlyTheKindOfHostOfEachRemote(t *testing.T) {
	r := submitRepo(t)
	r.git(nil, "remote", "add", "origin", "git@github.com:someone/private-thing.git")
	r.git(nil, "remote", "add", "mirror", "https://user@gitlab.com:8443/group/private-thing.git")
	r.git(nil, "remote", "add", "copy", "ssh://git@github.com/someone/private-thing")
	r.git(nil, "remote", "add", "local", "../elsewhere/private-thing")
	e, preview := submitEnv(t)
	e.Service().StoresRepoAssessment("ra-3")
	if got := e.RunInput(allAnswers+"yes\nyes\n", submitArgs(r.dir, preview)...); got.Exit != 0 {
		t.Fatalf("exit = %d, stderr %q", got.Exit, got.Stderr)
	}
	body := sentBody(t, e)
	var s repoassess.Submission
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Repo.RemoteHosts, []string{"github", "gitlab", "other"}) {
		t.Errorf("remote_hosts = %v", s.Repo.RemoteHosts)
	}
	for _, leak := range []string{"private-thing", "someone", "github.com", "gitlab.com", "user@"} {
		if bytes.Contains(body, []byte(leak)) {
			t.Errorf("the submission holds %q", leak)
		}
	}
}

func TestRepoSubmit_SendsNoLineCountsForAPartialClone(t *testing.T) {
	r := submitRepo(t)
	r.git(nil, "config", "uploadpack.allowFilter", "true")
	partial := filepath.Join(t.TempDir(), "assessed-partial")
	r.git(nil, "clone", "-q", "--filter=blob:none", "--no-checkout", "file://"+filepath.ToSlash(r.dir), partial)
	e, preview := submitEnv(t)
	e.Service().StoresRepoAssessment("ra-4")
	if got := e.RunInput(allAnswers+"yes\nyes\n", submitArgs(partial, preview)...); got.Exit != 0 {
		t.Fatalf("exit = %d, stderr %q", got.Exit, got.Stderr)
	}
	var s repoassess.Submission
	if err := json.Unmarshal(sentBody(t, e), &s); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range s.Commits.Items {
		for _, p := range c.Paths {
			n++
			if p.Added != nil || p.Deleted != nil {
				t.Errorf("%s has line counts %v / %v", p.Path, p.Added, p.Deleted)
			}
		}
	}
	if n != 4 || !slices.Equal(s.Repo.RemoteHosts, []string{"other"}) {
		t.Errorf("%d paths, remote hosts %v", n, s.Repo.RemoteHosts)
	}
}

func TestRepoSubmit_RefusesARepositoryWithSHA256Names(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on this machine")
	}
	r := newAssessRepo(t)
	dir := filepath.Join(t.TempDir(), "assessed-sha256")
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", "--object-format=sha256", dir).CombinedOutput(); err != nil {
		t.Skipf("this git cannot make a SHA-256 repository: %s", out)
	}
	r.dir = dir
	r.commit(change{files: []string{"a.go"}})
	e, preview := submitEnv(t)
	got := e.RunInput(allAnswers+"yes\nyes\n", submitArgs(dir, preview)...)
	if got.Exit != 1 || !strings.Contains(got.Stderr, "SHA-256") || !strings.Contains(got.Stdout, "Repository readout") {
		t.Errorf("exit = %d, stderr %q; want the readout and a refusal to send", got.Exit, got.Stderr)
	}
	assertNothingSent(t, e)
}

func TestRepoSubmit_NeverReplacesAnExistingFileWithThePreview(t *testing.T) {
	r := submitRepo(t)
	e, preview := submitEnv(t)
	if err := os.WriteFile(preview, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := e.RunInput(allAnswers+"yes\nyes\n", submitArgs(r.dir, preview)...)
	if got.Exit != 1 || !strings.Contains(got.Stderr, "already exists") {
		t.Errorf("exit = %d, stderr %q", got.Exit, got.Stderr)
	}
	if b, _ := os.ReadFile(preview); string(b) != "mine" {
		t.Errorf("the file now holds %q", b)
	}
	assertNothingSent(t, e)
}

func TestRepoSubmit_RefusesACommandLineItCannotRunAsWritten(t *testing.T) {
	r := submitRepo(t)
	e, preview := submitEnv(t)
	for _, args := range [][]string{
		{"repo", "assess", r.dir, "--submit"},
		{"repo", "assess", r.dir, "--submit", "--json", "--preview=" + preview},
		{"repo", "assess", r.dir, "--ownership=sole_author"},
		{"repo", "assess", r.dir, "--yes"},
		{"repo", "assess", r.dir, "--submit", "--preview=" + preview, "--ownership=mine"},
		{"repo", "assess", r.dir, "--submit", "--preview=" + preview, "--ai-share=unsure", "--ai-share=over_70"},
		{"repo", "assess", r.dir, "--submit", "--preview="},
		{"repo", "assess", r.dir, "--submit", "--preview", preview},
	} {
		got := e.Run(args...)
		if got.Exit != 2 || !strings.Contains(got.Stderr, "usage: trajector repo assess") {
			t.Errorf("%v: exit = %d, stderr %q; want the usage and 2", args[3:], got.Exit, got.Stderr)
		}
	}
	assertNothingSent(t, e)
}

func TestRepoSubmit_LeavesOutTheMessageLinesThatNameAPerson(t *testing.T) {
	r := submitRepo(t)
	r.commit(change{files: []string{"calc.go"}, day: 3, message: "Speed up the parser\n\nMeasured with the bench in calc_test.go.\n\n" +
		"Co-authored-by: Claude <noreply@anthropic.com>\nSigned-off-by: Carol Jones <carol@corp.invalid>\n" +
		"helped-BY: Dave\nCC: Erin <erin@corp.invalid>"})
	e, preview := submitEnv(t)
	e.Service().StoresRepoAssessment("ra-5")
	if got := e.RunInput(allAnswers+"yes\nyes\n", submitArgs(r.dir, preview)...); got.Exit != 0 {
		t.Fatalf("exit = %d, stderr %q", got.Exit, got.Stderr)
	}
	body := sentBody(t, e)
	var s repoassess.Submission
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	newest := s.Commits.Items[0]
	if newest.Message != "Speed up the parser\n\nMeasured with the bench in calc_test.go." || !newest.Agent {
		t.Errorf("message %q, agent %v; want the message without its trailers, and the agent still counted", newest.Message, newest.Agent)
	}
	for _, leak := range []string{"Claude", "Carol", "Dave", "Erin", "corp.invalid", "-by:", "CC:"} {
		if bytes.Contains(body, []byte(leak)) {
			t.Errorf("the submission holds %q", leak)
		}
	}
}

func TestRepoSubmit_ReadsAPathThatStartsWithTheCommitMark(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file name cannot hold a control character here")
	}
	r := submitRepo(t)
	// git lists a commit's paths in byte order and prints a newline
	// before the first, so only the second of two such names starts its
	// token with the mark.
	r.commit(change{files: []string{"\x1da.go", "\x1db.go"}, day: 3, message: "Add two odd names"})
	r.git(nil, "config", "uploadpack.allowFilter", "true")
	partial := filepath.Join(t.TempDir(), "assessed-partial")
	r.git(nil, "clone", "-q", "--filter=blob:none", "--no-checkout", "file://"+filepath.ToSlash(r.dir), partial)
	for name, dir := range map[string]string{"full": r.dir, "partial": partial} {
		t.Run(name, func(t *testing.T) {
			e, preview := submitEnv(t)
			e.Service().StoresRepoAssessment("ra-6")
			if got := e.RunInput(allAnswers+"yes\nyes\n", submitArgs(dir, preview)...); got.Exit != 0 {
				t.Fatalf("exit = %d, stderr %q", got.Exit, got.Stderr)
			}
			var s repoassess.Submission
			if err := json.Unmarshal(sentBody(t, e), &s); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, p := range s.Commits.Items[0].Paths {
				names = append(names, p.Path)
			}
			if len(s.Commits.Items) != 3 || !slices.Equal(names, []string{"\x1da.go", "\x1db.go"}) || s.Commits.Items[0].Message != "Add two odd names" {
				t.Errorf("%d commits, newest %q with paths %q", len(s.Commits.Items), s.Commits.Items[0].Message, names)
			}
		})
	}
}

// localObjects counts the objects a clone holds, without fetching any.
func localObjects(t *testing.T, dir string) int {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "cat-file", "--batch-all-objects", "--batch-check").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(out), "\n")
}

func TestRepoAssess_NeverFetchesWhatAPartialCloneLacks(t *testing.T) {
	r := submitRepo(t)
	r.git(nil, "config", "uploadpack.allowFilter", "true")
	t.Setenv("GIT_NO_LAZY_FETCH", "0")
	treeless := func(t *testing.T) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "assessed-treeless")
		r.git(nil, "clone", "-q", "--filter=tree:0", "--no-checkout", "file://"+filepath.ToSlash(r.dir), dir)
		return dir
	}

	t.Run("a treeless clone is refused in so many words", func(t *testing.T) {
		dir := treeless(t)
		for _, args := range [][]string{{"repo", "assess", dir}, submitArgs(dir, filepath.Join(t.TempDir(), "preview.json"), answerFlags...)} {
			e, _ := submitEnv(t)
			got := e.Run(args...)
			if got.Exit != 1 || !strings.Contains(got.Stderr, "treeless partial clone") || strings.Contains(got.Stderr, "exit status") {
				t.Errorf("%v: exit = %d, stderr %q", args[3:], got.Exit, got.Stderr)
			}
			assertNothingSent(t, e)
		}
	})

	// A clone that lacks trees but does not say it was filtered gets as
	// far as git log, which needs the trees, and must not fetch them:
	// not with the lazy fetch switch turned off, and not with the
	// developer's configuration allowing the transport.
	t.Run("a lazy fetch is blocked", func(t *testing.T) {
		dir := treeless(t)
		cmd := exec.CommandContext(t.Context(), "git", "-C", dir, "config", "--unset", "remote.origin.partialclonefilter")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		global := filepath.Join(t.TempDir(), "gitconfig")
		if err := os.WriteFile(global, []byte("[protocol \"file\"]\n\tallow = always\n[protocol]\n\tallow = always\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CONFIG_GLOBAL", global)
		before := localObjects(t, dir)
		got := assessEnv(t).Run("repo", "assess", dir)
		if got.Exit != 1 {
			t.Errorf("exit = %d, stdout %q", got.Exit, got.Stdout)
		}
		if after := localObjects(t, dir); after != before {
			t.Errorf("the clone held %d objects and now holds %d: something was fetched", before, after)
		}
	})
}
