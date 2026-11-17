package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/repoassess"
)

// previewFlag names the file the whole submission is written to before
// anything is sent. It is required: the submission can run to tens of
// megabytes of commit messages and paths, which a terminal cannot show,
// and a file the user names is one they can open, search and keep as
// the record of what was sent.
const previewFlag = "preview"

// answerFlag is the flag that answers question q without asking it.
func answerFlag(q repoassess.Question) string {
	return strings.ReplaceAll(q.Key, "_", "-")
}

// submitValueFlags are the --name=value flags --submit takes.
func submitValueFlags() []string {
	names := []string{previewFlag}
	for _, q := range repoassess.Questions {
		names = append(names, answerFlag(q))
	}
	return names
}

// takeValues strips each --name=value flag of names from args, wherever
// it stands. A flag given twice, or with nothing after the "=", is an
// error: neither has one reading.
func takeValues(args []string, names ...string) ([]string, map[string]string, error) {
	values := map[string]string{}
	kept := make([]string, 0, len(args))
	for _, arg := range args {
		name, value, ok := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !ok || !strings.HasPrefix(arg, "--") || !slices.Contains(names, name) {
			kept = append(kept, arg)
			continue
		}
		if value == "" {
			return nil, nil, fmt.Errorf("--%s needs a value after the =", name)
		}
		if _, twice := values[name]; twice {
			return nil, nil, fmt.Errorf("--%s is given twice", name)
		}
		values[name] = value
	}
	return kept, values, nil
}

// misused answers a command line that cannot be run as written.
func (a *app) misused(err error) int {
	fmt.Fprintln(a.stderr, repoUsage)
	fmt.Fprintf(a.stderr, "trajector: %v\n", err)
	return 2
}

// submitPlan is what the command line says about a submission before
// the repository is read.
type submitPlan struct {
	preview string
	// answers holds the questions answered by flag, by key.
	answers map[string]string
	// yes sends without asking for the confirmation. It never answers
	// the terms: those are accepted once, by a person who read them.
	yes bool
}

func newSubmitPlan(values map[string]string, yes bool) (submitPlan, error) {
	p := submitPlan{preview: values[previewFlag], answers: map[string]string{}, yes: yes}
	if p.preview == "" {
		return submitPlan{}, fmt.Errorf("--submit needs --%s=<file>: everything the submission sends is written there first, for you to read", previewFlag)
	}
	for _, q := range repoassess.Questions {
		v, ok := values[answerFlag(q)]
		if !ok {
			continue
		}
		if _, known := q.Option(v); !known {
			return submitPlan{}, fmt.Errorf("--%s=%s is not one of: %s", answerFlag(q), v, q.Values())
		}
		p.answers[q.Key] = v
	}
	return p, nil
}

// errNothingSent ends a submission the user stopped. What was said
// already explains it, so nothing more is printed.
var errNothingSent = errors.New("nothing was sent")

// submitAssessment asks the questions, writes the preview, shows the
// terms until they are accepted, asks for the confirmation, and sends.
// Each step that ends the submission says so; nothing is sent before
// the last one. A shallow clone ends it before the first step: its
// history is incomplete, so the readout and the root commit are too.
func (a *app) submitAssessment(ctx context.Context, dir string, r repoassess.Readout, p submitPlan) int {
	fmt.Fprintln(a.stdout)
	if r.Shallow {
		fmt.Fprintln(a.stderr, "trajector: this repository is a shallow clone, so its history is incomplete and it cannot be submitted. Run `git fetch --unshallow`, then run this command again. Nothing was sent.")
		return 1
	}
	m, err := a.machine()
	if err != nil {
		return a.fail(err)
	}
	a.tellNotices(m)
	if err := m.CanSubmitRepoAssessment(); err != nil {
		return a.fail(err)
	}
	in := bufio.NewReader(a.stdin)
	err = a.sendAssessment(ctx, m, in, dir, r, p)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errNothingSent):
		return 1
	default:
		return a.fail(err)
	}
}

func (a *app) sendAssessment(ctx context.Context, m *lifecycle.Machine, in *bufio.Reader, dir string, r repoassess.Readout, p submitPlan) error {
	answers, err := a.askQuestions(in, p.answers)
	if err != nil {
		return err
	}
	declaration, err := repoassess.NewDeclaration(answers)
	if err != nil {
		return err
	}
	repo, commits, err := repoassess.Collect(ctx, dir)
	if err != nil {
		return err
	}
	prepared, err := repoassess.Encode(repoassess.NewSubmission(consent.RepoTermsVersion, declaration, r, repo, commits))
	if err != nil {
		return err
	}
	if err := writePreview(p.preview, prepared.Body.Bytes()); err != nil {
		return err
	}
	printPreviewSummary(a.stdout, p.preview, prepared)

	if err := a.agreeRepoTerms(m, in, p.yes, p.preview); err != nil {
		return err
	}
	m.WarnNonDefaultEndpoint(a.stdout)
	if !p.yes {
		yes, err := askYes(a.stdout, in, "Send this submission now? [yes/no]: ")
		if err != nil {
			return err
		}
		if !yes {
			fmt.Fprintf(a.stdout, "Nothing was sent. The preview stays in %s.\n", p.preview)
			return errNothingSent
		}
	}
	id, err := m.SubmitRepoAssessment(ctx, prepared.Body)
	if err != nil {
		return submitFailure(err)
	}
	fmt.Fprintf(a.stdout, "Sent. Submission id: %s\n", id)
	fmt.Fprintln(a.stdout, "If trajector wants to talk about this repository, it writes to the email address of your trajector account.")
	fmt.Fprintf(a.stdout, "To have this submission deleted, email support@publicai.io with the id %s.\n", id)
	return nil
}

// askQuestions answers every question, in order: from its flag when
// one was given, otherwise by asking. An answer that stops ends the
// submission at once, before the next question is asked.
func (a *app) askQuestions(in *bufio.Reader, given map[string]string) (map[string]string, error) {
	answers := map[string]string{}
	for _, q := range repoassess.Questions {
		value, ok := given[q.Key]
		if !ok {
			var err error
			if value, err = a.ask(in, q); err != nil {
				return nil, err
			}
		}
		if o, _ := q.Option(value); o.Stops {
			fmt.Fprintln(a.stdout, "Only someone who may share this repository can submit it: its sole author, a member of the team that wrote it with everyone's consent, or someone who acts for the company that owns it. Nothing was sent.")
			return nil, errNothingSent
		}
		answers[q.Key] = value
	}
	return answers, nil
}

// ask puts one question and reads the answer: the option's number or
// its value. An answer that is neither asks again. Input that ends
// before an answer is an error that names the flag to answer with.
func (a *app) ask(in *bufio.Reader, q repoassess.Question) (string, error) {
	fmt.Fprintln(a.stdout)
	fmt.Fprintln(a.stdout, q.Text)
	for i, o := range q.Options {
		fmt.Fprintf(a.stdout, "  %d) %s\n", i+1, o.Text)
	}
	for {
		fmt.Fprintf(a.stdout, "Answer 1-%d: ", len(q.Options))
		line, err := in.ReadString('\n')
		answer := strings.TrimSpace(line)
		if n, convErr := strconv.Atoi(answer); convErr == nil && n >= 1 && n <= len(q.Options) {
			return q.Options[n-1].Value, nil
		}
		if o, ok := q.Option(answer); ok {
			return o.Value, nil
		}
		if err != nil {
			fmt.Fprintln(a.stdout)
			return "", fmt.Errorf("no answer to %q; to answer without a prompt, give --%s=<answer>, one of: %s; nothing was sent", q.Text, answerFlag(q), q.Values())
		}
		fmt.Fprintf(a.stdout, "%q is not one of the answers.\n", answer)
	}
}

// askYes puts a yes/no question. Only an explicit yes answers true.
func askYes(out io.Writer, in *bufio.Reader, prompt string) (bool, error) {
	fmt.Fprint(out, prompt)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return false, errors.New("no answer was given; nothing was sent")
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "yes", "y":
		return true, nil
	}
	return false, nil
}

// agreeRepoTerms shows the repository assessment terms in full until
// this version is accepted, and records the acceptance. --yes never
// accepts them: a run that asks nothing stops instead. The preview is
// written by then, and is kept either way.
func (a *app) agreeRepoTerms(m *lifecycle.Machine, in *bufio.Reader, yes bool, preview string) error {
	version, at, err := m.RepoTerms()
	if err != nil {
		return err
	}
	if version == consent.RepoTermsVersion {
		fmt.Fprintf(a.stdout, "You agreed to the repository assessment terms, version %s, on %s.\n", version, at)
		return nil
	}
	if yes {
		return fmt.Errorf("the repository assessment terms, version %s, are not agreed to on this device; run this command once without %s to read them; nothing was sent, and the preview stays in %s", consent.RepoTermsVersion, yesFlag, preview)
	}
	fmt.Fprintln(a.stdout)
	fmt.Fprintln(a.stdout, consent.RepoTermsText)
	fmt.Fprintln(a.stdout)
	agreed, err := askYes(a.stdout, in, "Do you agree to these terms? [yes/no]: ")
	if err != nil {
		return err
	}
	if !agreed {
		fmt.Fprintf(a.stdout, "Terms not agreed to. Nothing was sent. The preview stays in %s.\n", preview)
		return errNothingSent
	}
	return m.AcceptRepoTerms()
}

// writePreview writes the body, indented for reading, to a new file. An
// existing file is never replaced: the name may be a typo for a file
// the user keeps.
func writePreview(path string, body []byte) error {
	var indented bytes.Buffer
	if err := json.Indent(&indented, body, "", "  "); err != nil {
		return err
	}
	indented.WriteByte('\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists; name a new file; nothing was sent", path)
	}
	if err != nil {
		return fmt.Errorf("writing the preview: %w; nothing was sent", err)
	}
	if _, err := f.Write(indented.Bytes()); err != nil {
		f.Close()
		return fmt.Errorf("writing the preview: %w; nothing was sent", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing the preview: %w; nothing was sent", err)
	}
	return nil
}

// printPreviewSummary says where the whole submission is and what it
// holds in numbers, so the user knows what to look for in the file.
func printPreviewSummary(w io.Writer, path string, p repoassess.Prepared) {
	s := p.Submission
	paths, cutMessages, cutPaths := 0, 0, 0
	for _, c := range s.Commits.Items {
		paths += len(c.Paths)
		if c.MessageTruncated {
			cutMessages++
		}
		if c.PathsTruncated {
			cutPaths++
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Everything this submission sends is written to %s,\n", path)
	fmt.Fprintln(w, "indented for reading. Read it before you answer below.")
	if s.Commits.Truncated {
		fmt.Fprintf(w, "  commits:        the newest %d of %d, each with its message and changed paths\n", len(s.Commits.Items), s.Commits.Total)
	} else {
		fmt.Fprintf(w, "  commits:        all %d, each with its message and changed paths\n", len(s.Commits.Items))
	}
	fmt.Fprintf(w, "  changed paths:  %d\n", paths)
	if cutMessages > 0 {
		fmt.Fprintf(w, "  messages cut short: %d\n", cutMessages)
	}
	if cutPaths > 0 {
		fmt.Fprintf(w, "  commits with paths left out: %d\n", cutPaths)
	}
	fmt.Fprintf(w, "  remote hosts:   %s\n", hostsText(s.Repo.RemoteHosts))
	fmt.Fprintf(w, "  size:           %s, %s compressed\n", platform.HumanBytes(int64(p.Body.Len())), platform.HumanBytes(int64(p.GzipBytes)))
	fmt.Fprintln(w, "It holds no file content and no diff. Authors are numbers, not names or email addresses, and message lines")
	fmt.Fprintln(w, "that name a person, such as Co-authored-by or Signed-off-by, are left out. Messages and paths are masked for")
	fmt.Fprintln(w, "secrets, email addresses and phone numbers; any other name, path or URL written in a message is sent as written.")
}

func hostsText(hosts []string) string {
	if len(hosts) == 0 {
		return "none"
	}
	return strings.Join(hosts, ", ")
}

// submitFailure words a submission the service did not take.
func submitFailure(err error) error {
	e, ok := errors.AsType[*platform.RepoAssessmentError](err)
	if !ok {
		return err
	}
	var why string
	switch e.Outcome {
	case platform.RepoAssessmentRelogin:
		why = "the service did not accept this device's sign-in; run `trajector login`, then run this command again"
	case platform.RepoAssessmentUpgradeClient:
		why = "the service needs a newer trajector"
		if e.MinClientVersion != "" {
			why += ", version " + e.MinClientVersion + " or later"
		}
		if e.Message != "" {
			why += " (the service says: " + e.Message + ")"
		}
		why += "; run `trajector upgrade`, then run this command again"
	case platform.RepoAssessmentUpgradeTerms:
		why = "the service no longer takes submissions under the terms this version of trajector shows (version " + consent.RepoTermsVersion + "); run `trajector upgrade`, then run this command again"
	case platform.RepoAssessmentUnavailable:
		why = "the service does not take repository assessments now; try again later"
	case platform.RepoAssessmentTooLarge:
		why = "the submission is larger than the service takes"
	case platform.RepoAssessmentRetryLater:
		why = "the service asked to try again later"
		if e.RetryAfter > 0 {
			why += ", in " + e.RetryAfter.String() + " or more"
		}
	default:
		why = failureCause(e)
		if e.Code != "" {
			why += " (" + e.Code + ")"
		}
	}
	return fmt.Errorf("the submission did not go through: %s. Nothing is kept on this machine to retry it", why)
}

// failureCause words a failure with no outcome of its own. An answer
// from the service is named by its status code alone: the reason
// phrase after the code is the service's free text, which this client
// does not print as its own words.
func failureCause(e *platform.RepoAssessmentError) string {
	cause := errors.Unwrap(e)
	if status, ok := errors.AsType[*platform.StatusError](cause); ok {
		return fmt.Sprintf("the service answered with status %d", status.StatusCode)
	}
	if cause == nil {
		return "an error"
	}
	return cause.Error()
}
