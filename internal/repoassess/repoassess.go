// Package repoassess computes a readout of one git repository: counts
// about its commit history and about the paths in its tree, such as how
// many commits change tests and implementation code together.
//
// It reads commit metadata, the names of the paths each commit changed,
// and the path names of one tree. It reads no file content, writes
// nothing, and sends nothing anywhere. Author identities are used only
// to count and number authors; no name or email address leaves this
// package. A Readout holds no path; a Submission, which the user asks
// for one repository at a time, holds each commit's message and changed
// paths, masked by Encode.
//
// What a path holds (test, implementation code, other) is decided from
// the path alone, by the rules in rules.go; ruleVersion names them.
package repoassess

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Readout is what Assess computes. The JSON names are the field names
// a reader of the readout sees.
type Readout struct {
	RuleVersion int `json:"rule_version"`
	// AsOf is the moment the readout counts up to: commits authored at
	// or after it are left out, and the tree is the one HEAD's history
	// had before it.
	AsOf time.Time `json:"as_of"`
	// Shallow reports a shallow clone. Its history stops at the
	// shallow boundary, so every history count is short of the
	// repository's own.
	Shallow bool `json:"shallow"`

	// History: commits reachable from HEAD, merges left out.
	Commits        int     `json:"commits"`
	FirstCommit    string  `json:"first_commit"`
	SpanYears      float64 `json:"span_years"`
	ActiveMonths   int     `json:"active_months"`
	Authors        int     `json:"authors"`
	AuthorsGE3     int     `json:"authors_ge3"`
	TopAuthorShare float64 `json:"top_author_share"`
	// CommitsByYear counts the commits authored in each calendar year
	// (UTC), oldest year first. A year with no commit is left out, and
	// the counts add up to Commits.
	CommitsByYear    yearCounts `json:"commits_by_year"`
	CommitsLast3y    int        `json:"commits_last3y"`
	TestCommits      int        `json:"test_commits"`
	SrcCommits       int        `json:"src_commits"`
	Cochange         int        `json:"cochange"`
	CochangeSrc1to5  int        `json:"cochange_src1_5"`
	CochangeSrc2p    int        `json:"cochange_src2p"`
	CochangeLast3y   int        `json:"cochange_last3y"`
	AdjacentPairs    int        `json:"adjacent_pairs"`
	CochangeOrAdj    int        `json:"cochange_or_adj"`
	CochangeRate     float64    `json:"cochange_rate"`
	CICommits        int        `json:"ci_commits"`
	AgentCommitShare float64    `json:"agent_commit_share"`

	// Tree: the paths of the last commit before AsOf.
	Files              int     `json:"files"`
	SrcFiles           int     `json:"src_files"`
	TestFiles          int     `json:"test_files"`
	TestCodeFiles      int     `json:"test_code_files"`
	TestFileShare      float64 `json:"test_file_share"`
	MainLang           string  `json:"main_lang"`
	MainLangShare      float64 `json:"main_lang_share"`
	NLangs10pct        int     `json:"n_langs_10pct"`
	HasCI              bool    `json:"has_ci"`
	HasManifest        bool    `json:"has_manifest"`
	HasLock            bool    `json:"has_lock"`
	ManifestLockPaired bool    `json:"manifest_lock_paired"`
	HasDocker          bool    `json:"has_docker"`
}

// yearCount is the number of commits authored in one calendar year.
type yearCount struct {
	Year    int `json:"year"`
	Commits int `json:"commits"`
}

// yearCounts encodes as a JSON array even when it holds no year.
type yearCounts []yearCount

func (y yearCounts) MarshalJSON() ([]byte, error) {
	if y == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]yearCount(y))
}

var (
	// errNotDirectory reports a path that is not a directory.
	errNotDirectory = errors.New("the path given is not a directory")
	// errNotRepository reports a directory that is not inside a git
	// repository.
	errNotRepository = errors.New("not a git repository: give the path of one, or run this inside one")
	// errNoCommits reports a repository whose HEAD names no commit yet.
	errNoCommits = errors.New("the repository has no commit on HEAD yet")
	// errNoGit reports a machine where the git command cannot be found.
	errNoGit = errors.New("git is not installed or not on PATH; the readout is computed with git")
	// errTreeless reports a partial clone made without trees.
	errTreeless = errors.New("this is a treeless partial clone (cloned with --filter=tree:<depth>): the paths each commit changed are not on this machine, and this command never fetches them. Clone the repository again without --filter, or with --filter=blob:none, and run this command in that clone")
)

// commandTimeout bounds each git command. Reading the full history of a
// very large repository takes minutes; a command still running after
// this is taken to be stuck.
const commandTimeout = 10 * time.Minute

// Assess computes the readout of the repository that contains dir,
// counting up to now. A dir below the repository's top level assesses
// the whole repository.
func Assess(ctx context.Context, dir string, now time.Time) (Readout, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return Readout{}, errNotDirectory
	}
	if _, err := exec.LookPath("git"); err != nil {
		return Readout{}, errNoGit
	}
	g := gitRepo{dir: dir}
	state, err := g.output(ctx, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return Readout{}, errNotRepository
	}
	if _, err := g.output(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return Readout{}, errNoCommits
	}
	if err := g.refuseTreeless(ctx); err != nil {
		return Readout{}, err
	}
	cutoff := now.Unix()
	r := Readout{RuleVersion: ruleVersion, AsOf: time.Unix(cutoff, 0).UTC(), Shallow: strings.TrimSpace(state) == "true"}

	commits, err := g.history(ctx)
	if err != nil {
		return Readout{}, err
	}
	before := slices.DeleteFunc(commits, func(c commitRow) bool { return c.at >= cutoff })
	if len(before) == 0 {
		return r, nil
	}
	r.addHistory(before, cutoff)

	rev, err := g.output(ctx, "rev-list", "-1", "--before="+strconv.FormatInt(cutoff, 10), "HEAD")
	if err != nil {
		return Readout{}, err
	}
	var paths []string
	if rev = strings.TrimSpace(rev); rev != "" {
		if paths, err = g.treePaths(ctx, rev); err != nil {
			return Readout{}, err
		}
	}
	r.addTree(paths)
	return r, nil
}

// commitRow is one commit as the log reads it. The readout's history
// counts are an aggregate over these rows and read nothing else, so a
// value read per commit is added here and to logFormat, and the walk
// over the log stays as it is.
type commitRow struct {
	at     int64
	author string
	tests  int
	srcs   int
	ci     bool
	agent  bool
}

func (c commitRow) testOnly() bool { return c.tests > 0 && c.srcs == 0 }
func (c commitRow) srcOnly() bool  { return c.srcs > 0 && c.tests == 0 }
func (c commitRow) cochange() bool { return c.tests > 0 && c.srcs > 0 }

const (
	day          = 24 * 60 * 60
	threeYears   = 3 * 365 * day
	adjacentSpan = 3 * day
)

func (r *Readout) addHistory(cs []commitRow, cutoff int64) {
	n := len(cs)
	r.Commits = n
	first, last := cs[0].at, cs[n-1].at
	r.FirstCommit = time.Unix(first, 0).UTC().Format(time.DateOnly)
	r.SpanYears = round(float64(last-first)/day/365.25, 2)

	months := map[string]bool{}
	byAuthor := map[string]int{}
	agents := 0
	last3y := cutoff - threeYears
	for _, c := range cs {
		at := time.Unix(c.at, 0).UTC()
		months[at.Format("2006-01")] = true
		// cs is oldest first, so the years come in order.
		if k := len(r.CommitsByYear); k == 0 || r.CommitsByYear[k-1].Year != at.Year() {
			r.CommitsByYear = append(r.CommitsByYear, yearCount{Year: at.Year()})
		}
		r.CommitsByYear[len(r.CommitsByYear)-1].Commits++
		byAuthor[c.author]++
		if c.at >= last3y {
			r.CommitsLast3y++
		}
		if c.tests > 0 {
			r.TestCommits++
		}
		if c.srcs > 0 {
			r.SrcCommits++
		}
		if c.cochange() {
			r.Cochange++
			if c.srcs <= 5 {
				r.CochangeSrc1to5++
			}
			if c.srcs >= 2 {
				r.CochangeSrc2p++
			}
			if c.at >= last3y {
				r.CochangeLast3y++
			}
		}
		if c.ci {
			r.CICommits++
		}
		if c.agent {
			agents++
		}
	}
	r.ActiveMonths = len(months)
	r.Authors = len(byAuthor)
	top := 0
	for _, k := range byAuthor {
		if k >= 3 {
			r.AuthorsGE3++
		}
		top = max(top, k)
	}
	r.TopAuthorShare = share(top, n)
	r.AgentCommitShare = share(agents, n)

	// An implementation-only commit and a test-only commit next to each
	// other in time, by one author, close together, are one change
	// split over two commits.
	for i := 1; i < n; i++ {
		a, b := cs[i-1], cs[i]
		if a.author == b.author && b.at-a.at <= adjacentSpan &&
			(a.srcOnly() && b.testOnly() || a.testOnly() && b.srcOnly()) {
			r.AdjacentPairs++
		}
	}
	r.CochangeOrAdj = r.Cochange + r.AdjacentPairs
	r.CochangeRate = share(r.Cochange, n)
}

func (r *Readout) addTree(paths []string) {
	r.Files = len(paths)
	var langs []string
	perLang := map[string]int{}
	srcTotal := 0
	var manifests []string
	locks := map[string]bool{}
	for _, p := range paths {
		k, lang := classify(p)
		switch k {
		case test:
			r.TestFiles++
			if lang != "" {
				r.TestCodeFiles++
			}
		case src:
			r.SrcFiles++
			if perLang[lang] == 0 {
				langs = append(langs, lang)
			}
			perLang[lang]++
			srcTotal++
		case other:
		}
		if isCI(p) {
			r.HasCI = true
		}
		base := baseName(p)
		if strings.Count(p, "/") <= manifestMaxDepth {
			if isManifest(base) {
				manifests = append(manifests, base)
			}
			if lockNames[base] {
				locks[base] = true
			}
		}
		if isDocker(base) {
			r.HasDocker = true
		}
	}
	r.TestFileShare = round(float64(r.TestCodeFiles)/float64(max(1, r.SrcFiles+r.TestCodeFiles)), 3)
	// The first language seen wins a tie, so a tie reads the same on
	// every run.
	for _, lang := range langs {
		if perLang[lang] > perLang[r.MainLang] {
			r.MainLang = lang
		}
		if float64(perLang[lang]) >= 0.1*float64(srcTotal) {
			r.NLangs10pct++
		}
	}
	if r.MainLang != "" {
		r.MainLangShare = share(perLang[r.MainLang], srcTotal)
	}
	r.HasManifest = len(manifests) > 0
	r.HasLock = len(locks) > 0
	r.ManifestLockPaired = slices.ContainsFunc(manifests, func(m string) bool {
		return slices.ContainsFunc(manifestLocks[m], func(l string) bool { return locks[l] })
	})
}

func share(part, whole int) float64 { return round(float64(part)/float64(whole), 3) }

// round rounds x to digits decimal places, the nearest value to the
// exact binary x and the even one on an exact tie, so the printed
// readout is the same wherever it is computed.
func round(x float64, digits int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', digits, 64), 64)
	return v
}

// gitRepo runs git in one directory.
type gitRepo struct{ dir string }

// readOnlyCommands are the git commands this package runs, each named
// by the leading arguments that make it one. Each one only reads, so no
// argv assembled here can change the repository. config is allowed only
// in its listing form, which never writes.
var readOnlyCommands = [][]string{
	{"rev-parse"},
	{"rev-list"},
	{"log"},
	{"ls-tree"},
	{"config", "-z", "--get-regexp"},
}

func readOnly(args []string) bool {
	return slices.ContainsFunc(readOnlyCommands, func(prefix []string) bool {
		return len(args) >= len(prefix) && slices.Equal(args[:len(prefix)], prefix)
	})
}

// gitConfig is set on every command. Paths print as their bytes, not
// quoted; no rename detection pairs paths; signature checks, which run
// another program and print into the log, are off; no file system
// monitor is started; and no transport is allowed, so a partial clone
// cannot fetch an object it lacks (see gitEnv).
var gitConfig = []string{
	"-c", "core.quotepath=off",
	"-c", "diff.renames=false",
	"-c", "log.showSignature=false",
	"-c", "core.fsmonitor=false",
	"-c", "protocol.allow=never",
}

// redirectingEnv are the variables that make git read another
// repository, other objects or other refs than those of the repository
// dir is in: its location (GIT_DIR, GIT_WORK_TREE, GIT_COMMON_DIR,
// GIT_INDEX_FILE), its objects (GIT_OBJECT_DIRECTORY,
// GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_QUARANTINE_PATH), its refs and
// history (GIT_NAMESPACE, GIT_REPLACE_REF_BASE, GIT_NO_REPLACE_OBJECTS,
// GIT_GRAFT_FILE, GIT_SHALLOW_FILE). Each is removed from the
// environment of every command, and so are the two noNetworkEnv sets.
var redirectingEnv = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_QUARANTINE_PATH",
	"GIT_NAMESPACE", "GIT_REPLACE_REF_BASE", "GIT_NO_REPLACE_OBJECTS", "GIT_GRAFT_FILE", "GIT_SHALLOW_FILE",
}

// noNetworkEnv keeps git off the network whatever its version and its
// configuration. GIT_NO_LAZY_FETCH stops a partial clone from fetching
// an object it lacks, in a git that knows the variable. An empty
// GIT_ALLOW_PROTOCOL allows no transport at all, and overrides any
// protocol.<name>.allow a configuration file sets, which
// protocol.allow=never alone does not.
var noNetworkEnv = []string{"GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL="}

func (g gitRepo) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	if !readOnly(args) {
		return nil, fmt.Errorf("repoassess: git %q is not one of the read-only commands", strings.Join(args, " "))
	}
	argv := append(append([]string{"-C", g.dir}, gitConfig...), args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(redirectingEnv, name) || slices.ContainsFunc(noNetworkEnv, func(set string) bool { return strings.HasPrefix(set, name+"=") })
	}), noNetworkEnv...)
	return cmd, nil
}

func (g gitRepo) output(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd, err := g.command(ctx, args...)
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		return "", g.failure(ctx, args[0], err)
	}
	return string(out), nil
}

// refuseTreeless reports errTreeless for a partial clone whose filter
// leaves out trees: git can list no commit's changed paths without
// fetching them, and fetching is never allowed here.
func (g gitRepo) refuseTreeless(ctx context.Context) error {
	filters, err := g.config(ctx, `^(remote\..*\.partialclonefilter|core\.partialclonefilter)$`)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(filters, func(kv [2]string) bool { return strings.HasPrefix(strings.TrimSpace(kv[1]), "tree:") }) {
		return errTreeless
	}
	return nil
}

// failure names a git command that did not finish. What git printed on
// stderr is not repeated: it can name the repository's location.
func (gitRepo) failure(ctx context.Context, sub string, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("git %s did not finish within %v", sub, commandTimeout)
	}
	return fmt.Errorf("git %s failed: %w", sub, err)
}

// headerMark starts each commit's line in the log; the paths the commit
// changed follow it, one per line. git quotes a path that holds a
// control character, so no path line can start with it. trailerMark
// separates the Co-authored-by values from the rest of that line, and
// trailerSep the values from each other.
const (
	headerMark  = "\x1d"
	trailerMark = "\x1e"
	trailerSep  = "\x1f"
)

const logFormat = "--format=" + headerMark + "%at\t%ae\t%an" + trailerMark +
	"%(trailers:key=Co-authored-by,valueonly,unfold,separator=%x1f)"

// history reads every non-merge commit reachable from HEAD, oldest
// first. Commits with the same author time keep the order git printed
// them in.
func (g gitRepo) history(ctx context.Context) ([]commitRow, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd, err := g.command(ctx, "log", "HEAD", "--no-merges", "--no-renames", "--no-color", "--name-only", logFormat)
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, g.failure(ctx, "log", err)
	}
	commits, parseErr := parseLog(stdout)
	// Whatever the parse left unread is drained, so git is never left
	// blocked on a full pipe.
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return nil, g.failure(ctx, "log", err)
	}
	if parseErr != nil {
		return nil, parseErr
	}
	slices.SortStableFunc(commits, func(a, b commitRow) int { return cmp.Compare(a.at, b.at) })
	return commits, nil
}

func parseLog(r io.Reader) ([]commitRow, error) {
	var commits []commitRow
	var cur *commitRow
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, headerMark):
			c, perr := parseHeader(line[len(headerMark):])
			if perr != nil {
				return nil, perr
			}
			commits = append(commits, c)
			cur = &commits[len(commits)-1]
		case line != "" && cur != nil:
			switch k, _ := classify(line); k {
			case test:
				cur.tests++
			case src:
				cur.srcs++
			case other:
			}
			if isCI(line) {
				cur.ci = true
			}
		}
		if errors.Is(err, io.EOF) {
			return commits, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// parseHeader reads "<author time>\t<email>\t<name>" and the trailer
// values after trailerMark. An author is counted by email address, by
// name when the address is empty.
func parseHeader(s string) (commitRow, error) {
	fields, trailers, _ := strings.Cut(s, trailerMark)
	parts := append(strings.Split(fields, "\t"), "", "")
	at, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return commitRow{}, fmt.Errorf("git log printed a commit time that is not a number: %w", err)
	}
	email, name := strings.ToLower(parts[1]), parts[2]
	c := commitRow{at: at, author: cmp.Or(email, strings.ToLower(name))}
	c.agent = isAgentAddress(email)
	for value := range strings.SplitSeq(trailers, trailerSep) {
		if isAgentAddress(trailerAddress(value)) {
			c.agent = true
		}
	}
	return c, nil
}

// trailerAddress is the lowercase email address in a "Name <address>"
// trailer value, or "" when it has none.
func trailerAddress(value string) string {
	open := strings.LastIndexByte(value, '<')
	if open < 0 {
		return ""
	}
	addr, _, ok := strings.Cut(value[open+1:], ">")
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr))
}

// treePaths lists every path in rev's tree, from the repository root
// whatever directory git runs in.
func (g gitRepo) treePaths(ctx context.Context, rev string) ([]string, error) {
	out, err := g.output(ctx, "ls-tree", "-r", "--full-tree", "--name-only", rev)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n"), nil
}
