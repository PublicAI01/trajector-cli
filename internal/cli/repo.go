package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/repoassess"
)

const repoUsage = `usage: trajector repo assess [<path>] [--json]
       trajector repo assess [<path>] --submit --preview=<file> [--ownership=<answer>]
              [--ever-public=<answer>] [--in-production=<answer>] [--ai-share=<answer>] [--yes]`

const (
	jsonFlag   = "--json"
	submitFlag = "--submit"
	yesFlag    = "--yes"
)

// repoCmd hosts the commands about a git repository the user names.
// None of them needs any project enabled. Only --submit reads this
// device's state: its pairing, and the repository assessment terms the
// user accepted, which it records.
func (a *app) repoCmd(ctx context.Context, args []string) int {
	if code, answered := a.preparse(repoUsage, args[:min(1, len(args))], nil); answered {
		return code
	}
	if len(args) == 0 || args[0] != "assess" {
		fmt.Fprintln(a.stderr, repoUsage)
		return 2
	}
	return a.assessCmd(ctx, args[1:])
}

// assessCmd prints the readout of one repository: the one that contains
// the path given, or the working directory. With --submit it then sends
// that repository's submission, once the user has seen all of it and
// confirmed.
func (a *app) assessCmd(ctx context.Context, args []string) int {
	args, asJSON := takeAnywhere(args, jsonFlag)
	args, submit := takeAnywhere(args, submitFlag)
	args, yes := takeAnywhere(args, yesFlag)
	args, values, err := takeValues(args, submitValueFlags()...)
	if err != nil {
		return a.misused(err)
	}
	if code, answered := a.preparse(repoUsage, args, nil); answered {
		return code
	}
	if len(args) > 1 || submit && asJSON || !submit && (yes || len(values) > 0) {
		fmt.Fprintln(a.stderr, repoUsage)
		return 2
	}
	var plan submitPlan
	if submit {
		if plan, err = newSubmitPlan(values, yes); err != nil {
			return a.misused(err)
		}
	}
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}
	now, pinned, err := pinnedNow()
	if err != nil {
		return a.fail(err)
	}
	if !pinned {
		now = time.Now()
	}
	r, err := repoassess.Assess(ctx, dir, now)
	if err != nil {
		return a.fail(err)
	}
	if asJSON {
		out, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return a.fail(err)
		}
		fmt.Fprintf(a.stdout, "%s\n", out)
		return 0
	}
	printReadout(a.stdout, r)
	if !submit {
		return 0
	}
	return a.submitAssessment(ctx, dir, r, plan)
}

// readoutRow is one line of the printed readout: the field's JSON name,
// its value, and what it counts.
type readoutRow struct {
	name, value, meaning string
}

func readoutRows(r repoassess.Readout) []readoutRow {
	i := strconv.Itoa
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	b := strconv.FormatBool
	return []readoutRow{
		{"commits", i(r.Commits), "commits reachable from HEAD, merges left out"},
		{"first_commit", r.FirstCommit, "author date of the earliest commit (UTC)"},
		{"span_years", f(r.SpanYears), "years from the earliest to the latest commit"},
		{"active_months", i(r.ActiveMonths), "calendar months with at least one commit"},
		{"authors", i(r.Authors), "distinct author email addresses"},
		{"authors_ge3", i(r.AuthorsGE3), "authors with 3 or more commits"},
		{"top_author_share", f(r.TopAuthorShare), "share of commits by the author with the most"},
		{"commits_by_year", yearCounts(r), "commits authored in each calendar year (UTC)"},
		{"commits_last3y", i(r.CommitsLast3y), "commits authored in the last 3 years"},
		{"test_commits", i(r.TestCommits), "commits that change a test path"},
		{"src_commits", i(r.SrcCommits), "commits that change an implementation file"},
		{"cochange", i(r.Cochange), "commits that change tests and implementation together"},
		{"cochange_src1_5", i(r.CochangeSrc1to5), "of those, commits that change 1 to 5 implementation files"},
		{"cochange_src2p", i(r.CochangeSrc2p), "of those, commits that change 2 or more implementation files"},
		{"cochange_last3y", i(r.CochangeLast3y), "of those, commits authored in the last 3 years"},
		{"adjacent_pairs", i(r.AdjacentPairs), "an implementation-only and a test-only commit in a row, one author, at most 3 days apart"},
		{"cochange_or_adj", i(r.CochangeOrAdj), "cochange plus adjacent_pairs"},
		{"cochange_rate", f(r.CochangeRate), "cochange divided by commits"},
		{"ci_commits", i(r.CICommits), "commits that change CI configuration"},
		{"agent_commit_share", f(r.AgentCommitShare), "share of commits with a coding agent as author or Co-authored-by"},
		{"files", i(r.Files), "paths in the tree"},
		{"src_files", i(r.SrcFiles), "implementation files in the tree"},
		{"test_files", i(r.TestFiles), "test paths in the tree"},
		{"test_code_files", i(r.TestCodeFiles), "test paths that are code"},
		{"test_file_share", f(r.TestFileShare), "test_code_files divided by src_files plus test_code_files"},
		{"main_lang", r.MainLang, "language of the most implementation files"},
		{"main_lang_share", f(r.MainLangShare), "share of implementation files in main_lang"},
		{"n_langs_10pct", i(r.NLangs10pct), "languages with at least a tenth of the implementation files"},
		{"has_ci", b(r.HasCI), "the tree has CI configuration"},
		{"has_manifest", b(r.HasManifest), "a dependency manifest at most 2 directories deep"},
		{"has_lock", b(r.HasLock), "a lockfile at most 2 directories deep"},
		{"manifest_lock_paired", b(r.ManifestLockPaired), "a manifest together with a lockfile that pins it"},
		{"has_docker", b(r.HasDocker), "a Dockerfile or compose file anywhere in the tree"},
	}
}

// yearCounts prints the commit count of each year, in the readout's
// order.
func yearCounts(r repoassess.Readout) string {
	var b strings.Builder
	for i, y := range r.CommitsByYear {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%d:%d", y.Year, y.Commits)
	}
	return b.String()
}

// printReadout prints the readout for a person. It states only what was
// counted, as of when, and by which rules.
func printReadout(w io.Writer, r repoassess.Readout) {
	fmt.Fprintf(w, "Repository readout, rule version %d, as of %s\n", r.RuleVersion, r.AsOf.Format(time.RFC3339))
	fmt.Fprintln(w, "Read from git history and file paths only. Nothing was sent.")
	if r.Shallow {
		fmt.Fprintln(w, "This clone is shallow: commits before its boundary are not counted, so the history counts are incomplete.")
		fmt.Fprintln(w, "Run git fetch --unshallow in it for a complete readout.")
	}
	if r.Commits == 0 {
		fmt.Fprintln(w, "No commit on HEAD was authored before that time.")
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range readoutRows(r) {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", row.name, row.value, row.meaning)
	}
	tw.Flush()
}
