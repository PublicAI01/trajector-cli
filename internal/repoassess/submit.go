package repoassess

import (
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/PublicAI01/trajector-cli/internal/redact"
)

// Submission is what `repo assess --submit` sends about one repository:
// the readout, the repository's identity, its commits, and the user's
// answers. The field order is the order of the keys on the wire.
type Submission struct {
	Format         int         `json:"format"`
	ConsentVersion string      `json:"consent_version"`
	Repo           Repo        `json:"repo"`
	Declaration    Declaration `json:"declaration"`
	Readout        Readout     `json:"readout"`
	Commits        Commits     `json:"commits"`
}

// submissionFormat is the version of the Submission layout.
const submissionFormat = 1

// Repo identifies the repository without naming where it is.
type Repo struct {
	// RootCommit is the root commit reachable from HEAD; of several,
	// the one with the earliest author time.
	RootCommit string `json:"root_commit"`
	HeadCommit string `json:"head_commit"`
	// RemoteHosts is the kind of host of each remote, from
	// remoteHostKinds, without repeats and in byte order. A URL is
	// never kept.
	RemoteHosts []string `json:"remote_hosts"`
}

// Commits are the repository's non-merge commits, newest first.
type Commits struct {
	// Total counts every non-merge commit reachable from HEAD.
	Total int `json:"total"`
	// Truncated is true exactly when Items holds fewer than Total.
	Truncated bool     `json:"truncated"`
	Items     []Commit `json:"items"`
}

// Commit is one commit. Author is a number that stands for one author
// within the submission; the author's name and email address are not
// part of it.
type Commit struct {
	ID               string `json:"id"`
	AuthoredAt       int64  `json:"authored_at"`
	Author           int    `json:"author"`
	Agent            bool   `json:"agent"`
	Message          string `json:"message"`
	MessageTruncated bool   `json:"message_truncated"`
	// Paths are the paths the commit changed, in the order git printed
	// them. PathCount counts all of them, including those left out;
	// PathsTruncated is true exactly when PathCount is larger than
	// len(Paths).
	Paths          []ChangedPath `json:"paths"`
	PathCount      int           `json:"path_count"`
	PathsTruncated bool          `json:"paths_truncated"`
}

// ChangedPath is one path a commit changed, relative to the repository
// root. Added and Deleted are nil when there is no line count: a binary
// file, or a partial clone, whose file content is never read.
type ChangedPath struct {
	Path    string `json:"path"`
	Added   *int   `json:"added"`
	Deleted *int   `json:"deleted"`
}

// The bounds a submission keeps, in the units the service counts.
const (
	maxItems          = 20000
	maxMessageBytes   = 2000
	maxPathsPerCommit = 200
	maxPathBytes      = 1024
	maxBodyBytes      = 48 << 20
	maxGzipBytes      = 16 << 20
)

// errSHA256Repository reports a repository whose object names are
// SHA-256. A submission names commits by their SHA-1 ids only.
var errSHA256Repository = errors.New("this repository uses SHA-256 object names, which a submission cannot carry; the readout above is complete, and nothing was sent")

// NewSubmission puts the parts of a submission together.
func NewSubmission(consentVersion string, d Declaration, r Readout, repo Repo, commits Commits) Submission {
	return Submission{
		Format:         submissionFormat,
		ConsentVersion: consentVersion,
		Repo:           repo,
		Declaration:    d,
		Readout:        r,
		Commits:        commits,
	}
}

// Collect reads the identity and the commits of the repository that
// contains dir. Like Assess it runs only read-only git commands and
// reads no file content, with one exception it states: the line counts
// of each changed path, which git computes from the content. A partial
// clone gets no line counts, because the content it lacks would be
// fetched over the network.
func Collect(ctx context.Context, dir string) (Repo, Commits, error) {
	g := gitRepo{dir: dir}
	head, err := g.output(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return Repo{}, Commits{}, errNoCommits
	}
	repo := Repo{HeadCommit: strings.TrimSpace(head)}
	if len(repo.HeadCommit) != 40 {
		return Repo{}, Commits{}, errSHA256Repository
	}
	if err := g.refuseTreeless(ctx); err != nil {
		return Repo{}, Commits{}, err
	}
	if repo.RootCommit, err = g.rootCommit(ctx); err != nil {
		return Repo{}, Commits{}, err
	}
	config, err := g.config(ctx, `^(remote\..*\.(url|promisor)|extensions\.partialclone)$`)
	if err != nil {
		return Repo{}, Commits{}, err
	}
	repo.RemoteHosts = remoteHosts(config)
	partial := slices.ContainsFunc(config, func(kv [2]string) bool {
		return kv[0] == "extensions.partialclone" || strings.HasSuffix(kv[0], ".promisor") && strings.EqualFold(kv[1], "true")
	})

	count, err := g.output(ctx, "rev-list", "--count", "--no-merges", "HEAD")
	if err != nil {
		return Repo{}, Commits{}, err
	}
	total, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil {
		return Repo{}, Commits{}, fmt.Errorf("git rev-list printed a count that is not a number: %w", err)
	}
	items, err := g.commits(ctx, !partial)
	if err != nil {
		return Repo{}, Commits{}, err
	}
	return repo, Commits{Total: total, Truncated: total > len(items), Items: items}, nil
}

// rootCommit is the root commit reachable from HEAD with the earliest
// author time; of equal times, the smallest id, so the answer does not
// depend on the order git prints them in.
func (g gitRepo) rootCommit(ctx context.Context) (string, error) {
	out, err := g.output(ctx, "log", "HEAD", "--max-parents=0", "--no-color", "--format=%H %at")
	if err != nil {
		return "", err
	}
	type root struct {
		id string
		at int64
	}
	var roots []root
	for line := range strings.FieldsFuncSeq(out, func(r rune) bool { return r == '\n' }) {
		id, at, _ := strings.Cut(line, " ")
		t, err := strconv.ParseInt(at, 10, 64)
		if err != nil {
			return "", fmt.Errorf("git log printed a commit time that is not a number: %w", err)
		}
		roots = append(roots, root{id, t})
	}
	if len(roots) == 0 {
		return "", errNoCommits
	}
	return slices.MinFunc(roots, func(a, b root) int { return cmp.Or(cmp.Compare(a.at, b.at), cmp.Compare(a.id, b.id)) }).id, nil
}

// config lists the configuration entries whose names match pattern, as
// name and value. No match is an empty list, not an error.
func (g gitRepo) config(ctx context.Context, pattern string) ([][2]string, error) {
	out, err := g.output(ctx, "config", "-z", "--get-regexp", pattern)
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries [][2]string
	for entry := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		name, value, _ := strings.Cut(entry, "\n")
		entries = append(entries, [2]string{name, value})
	}
	return entries, nil
}

// remoteHostKinds maps a host name, and every name below it, to the
// kind of host a submission states. Any other host is "other".
var remoteHostKinds = map[string]string{
	"github.com":    "github",
	"gitlab.com":    "gitlab",
	"bitbucket.org": "bitbucket",
}

// remoteHosts classifies the host of each remote.<name>.url entry.
func remoteHosts(config [][2]string) []string {
	kinds := []string{}
	for _, kv := range config {
		if !strings.HasPrefix(kv[0], "remote.") || !strings.HasSuffix(kv[0], ".url") {
			continue
		}
		kind := hostKind(urlHost(kv[1]))
		if !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	slices.Sort(kinds)
	return kinds
}

func hostKind(host string) string {
	host = strings.ToLower(host)
	for name, kind := range remoteHostKinds {
		if host == name || strings.HasSuffix(host, "."+name) {
			return kind
		}
	}
	return "other"
}

// urlHost reads the host of a remote URL in the forms git accepts:
// scheme://[user@]host[:port]/path and the scp-like [user@]host:path. A
// local path has no host and reads as "".
func urlHost(u string) string {
	if _, rest, ok := strings.Cut(u, "://"); ok {
		host, _, _ := strings.Cut(rest, "/")
		if i := strings.LastIndexByte(host, '@'); i >= 0 {
			host = host[i+1:]
		}
		if strings.HasPrefix(host, "[") {
			end, _, _ := strings.Cut(host[1:], "]")
			return end
		}
		host, _, _ = strings.Cut(host, ":")
		return host
	}
	colon := strings.IndexByte(u, ':')
	if colon < 0 || strings.Contains(u[:colon], "/") {
		return ""
	}
	host := u[:colon]
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	return host
}

// The marks of the commit log Collect reads. With -z, git ends the
// message and each changed path with a NUL byte. Each commit's record
// starts with one more NUL byte, so an empty token comes before every
// commit and before nothing else: a path is never empty. The token
// after it is the commit's: commitMark and its id, then fieldMark
// before the header that parseHeader reads and before the message.
// Since only the place of a token marks a commit, a path may start
// with any byte, commitMark included.
const (
	commitMark = "\x1d"
	fieldMark  = "\x1c"
)

const commitLogFormat = "--format=%x00" + commitMark + "%H" + fieldMark + "%at\t%ae\t%an" + trailerMark +
	"%(trailers:key=Co-authored-by,valueonly,unfold,separator=%x1f)" + fieldMark + "%B"

// commits reads the newest non-merge commits reachable from HEAD, up to
// maxItems, newest first. With counts it asks git for the lines each
// path added and deleted; without, only for the paths.
func (g gitRepo) commits(ctx context.Context, counts bool) ([]Commit, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	changes := "--name-only"
	if counts {
		changes = "--numstat"
	}
	cmd, err := g.command(ctx, "log", "HEAD", "--no-merges", "--no-renames", "--no-color", "--no-ext-diff", "--no-textconv",
		"-z", "-n", strconv.Itoa(maxItems), changes, commitLogFormat)
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
	commits, authors, parseErr := parseCommitLog(stdout, counts)
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return nil, g.failure(ctx, "log", err)
	}
	if parseErr != nil {
		return nil, parseErr
	}
	numberAuthors(commits, authors)
	return commits, nil
}

// parseCommitLog reads the -z log. It returns the commits and, for
// each, the key its author is counted by, which never leaves this
// package.
func parseCommitLog(r io.Reader, counts bool) ([]Commit, []string, error) {
	var commits []Commit
	var authors []string
	br := bufio.NewReader(r)
	// next says what the coming token is: a commit's record, the first
	// path after it, which git prints after a newline, or a later path.
	const (
		record = iota
		firstPath
		path
	)
	next := path
	for {
		token, err := br.ReadString(0)
		token = strings.TrimSuffix(token, "\x00")
		switch {
		case next == record:
			after, ok := strings.CutPrefix(token, commitMark)
			parts := strings.SplitN(after, fieldMark, 3)
			if !ok || len(parts) != 3 {
				return nil, nil, errors.New("git log printed a commit this command cannot read")
			}
			row, perr := parseHeader(parts[1])
			if perr != nil {
				return nil, nil, perr
			}
			commits = append(commits, Commit{
				ID:         parts[0],
				AuthoredAt: row.at,
				Agent:      row.agent,
				Message:    strings.TrimRight(parts[2], "\n"),
				Paths:      []ChangedPath{},
			})
			authors = append(authors, row.author)
			next = firstPath
		case token == "":
			next = record
		case len(commits) > 0:
			if next == firstPath {
				token = strings.TrimPrefix(token, "\n")
				next = path
			}
			if p, ok := parseChange(token, counts); ok && token != "" {
				c := &commits[len(commits)-1]
				c.Paths = append(c.Paths, p)
				c.PathCount++
			}
		}
		if errors.Is(err, io.EOF) {
			return commits, authors, nil
		}
		if err != nil {
			return nil, nil, err
		}
	}
}

// parseChange reads one changed path: "<added>\t<deleted>\t<path>"
// with counts, where git prints "-" for a binary file, and the bare
// path without.
func parseChange(token string, counts bool) (ChangedPath, bool) {
	if !counts {
		return ChangedPath{Path: token}, true
	}
	added, rest, ok1 := strings.Cut(token, "\t")
	deleted, path, ok2 := strings.Cut(rest, "\t")
	if !ok1 || !ok2 || path == "" {
		return ChangedPath{}, false
	}
	return ChangedPath{Path: path, Added: lineCount(added), Deleted: lineCount(deleted)}, true
}

func lineCount(s string) *int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return nil
	}
	return new(n)
}

// numberAuthors gives each author a number in the order of their first
// commit, oldest first, so that a repository's first author is 0 and a
// later commit does not renumber the earlier ones.
func numberAuthors(commits []Commit, authors []string) {
	numbers := map[string]int{}
	for i := len(commits) - 1; i >= 0; i-- {
		n, ok := numbers[authors[i]]
		if !ok {
			n = len(numbers)
			numbers[authors[i]] = n
		}
		commits[i].Author = n
	}
}

// Prepared is a submission made ready to send: its body, masked and
// bounded, and the submission that body encodes.
type Prepared struct {
	Body       redact.RedactedBytes
	Submission Submission
	// GzipBytes is the size of the body compressed for sending.
	GzipBytes int
}

// Encode masks and bounds a submission and serializes it. Each commit
// message first loses the lines that name a person (see
// withoutPersonLines); then each message and each path passes the
// redaction pass every upload uses, and only then is cut to its bound,
// so a secret is masked before any part of it can be cut away. The
// body is compact JSON in field order.
func Encode(s Submission) (Prepared, error) {
	return encode(s, maxBodyBytes, maxGzipBytes)
}

// encode is Encode with the body bounds as arguments: when the body is
// larger than maxBody bytes, or than maxGzip bytes compressed, the
// oldest commits are left out until it fits.
func encode(s Submission, maxBody, maxGzip int) (Prepared, error) {
	items := append([]Commit{}, s.Commits.Items[:min(len(s.Commits.Items), maxItems)]...)
	for i := range items {
		items[i].Message = withoutPersonLines(items[i].Message)
	}
	if err := mask(items); err != nil {
		return Prepared{}, err
	}
	for i := range items {
		items[i] = bound(items[i])
	}
	s.Commits.Total = max(s.Commits.Total, len(s.Commits.Items))
	if s.Repo.RemoteHosts == nil {
		s.Repo.RemoteHosts = []string{}
	}
	for {
		s.Commits.Items = items
		s.Commits.Truncated = s.Commits.Total > len(items)
		body, err := encodeJSON(s)
		if err != nil {
			return Prepared{}, err
		}
		zipped, err := gzipSize(body)
		if err != nil {
			return Prepared{}, err
		}
		if len(body) <= maxBody && zipped <= maxGzip {
			return Prepared{Body: redact.AlreadyRedacted(body), Submission: s, GzipBytes: zipped}, nil
		}
		if len(items) == 0 {
			return Prepared{}, errors.New("the readout alone is larger than a submission can be")
		}
		// The oldest commits go first. The share kept is a little under
		// the share that fits, so few rounds reach the bound.
		fits := min(float64(maxBody)/float64(len(body)), float64(maxGzip)/float64(zipped))
		items = items[:min(len(items)-1, int(float64(len(items))*fits*0.95))]
	}
}

// personLine is a message line that names a person in a trailer:
// "<word>-by: …" (Co-authored-by, Signed-off-by, Acked-by and the
// like) or "Cc: …", in any case.
var personLine = regexp.MustCompile(`(?i)^[ \t]*(?:[a-z0-9][a-z0-9-]*-by|cc)[ \t]*:`)

// withoutPersonLines drops every personLine from message. Those lines
// hold the names and email addresses of people other than the user,
// who never agreed to send them. Whether a coding agent co-authored the
// commit is read before, from the trailers git parses. A message that
// loses a line also loses the blank lines that end it.
func withoutPersonLines(message string) string {
	lines := strings.Split(message, "\n")
	kept := slices.DeleteFunc(slices.Clone(lines), personLine.MatchString)
	if len(kept) == len(lines) {
		return message
	}
	return strings.TrimRight(strings.Join(kept, "\n"), " \t\r\n")
}

// mask runs every message and path of items through the redaction
// pass, one line per commit.
func mask(items []Commit) error {
	var lines bytes.Buffer
	for _, c := range items {
		written := make([]string, 0, len(c.Paths)+1)
		written = append(written, strings.ToValidUTF8(c.Message, "�"))
		for _, p := range c.Paths {
			written = append(written, strings.ToValidUTF8(p.Path, "�"))
		}
		line, err := json.Marshal(written)
		if err != nil {
			return err
		}
		lines.Write(line)
		lines.WriteByte('\n')
	}
	if len(items) == 0 {
		return nil
	}
	masked, err := redact.NewMasker().JSONLBytes(lines.Bytes())
	if err != nil {
		return err
	}
	out := bytes.Split(bytes.TrimSuffix(masked.Bytes(), []byte{'\n'}), []byte{'\n'})
	if len(out) != len(items) {
		return errors.New("masking changed how many commits a submission carries")
	}
	for i, line := range out {
		var written []string
		if err := json.Unmarshal(line, &written); err != nil {
			return fmt.Errorf("reading a masked commit back: %w", err)
		}
		if len(written) != len(items[i].Paths)+1 {
			return errors.New("masking changed how many strings a commit carries")
		}
		items[i].Message = written[0]
		paths := slices.Clone(items[i].Paths)
		for j := range paths {
			paths[j].Path = written[j+1]
		}
		items[i].Paths = paths
	}
	return nil
}

// bound cuts one commit to the bounds: the message to maxMessageBytes on
// a character boundary, and the paths to the first maxPathsPerCommit
// that are no longer than maxPathBytes and are relative.
func bound(c Commit) Commit {
	if len(c.Message) > maxMessageBytes {
		cut := maxMessageBytes
		for cut > 0 && !utf8.RuneStart(c.Message[cut]) {
			cut--
		}
		c.Message = c.Message[:cut]
		c.MessageTruncated = true
	}
	c.PathCount = max(c.PathCount, len(c.Paths))
	kept := make([]ChangedPath, 0, min(len(c.Paths), maxPathsPerCommit))
	for _, p := range c.Paths {
		if len(kept) == maxPathsPerCommit {
			break
		}
		if len(p.Path) <= maxPathBytes && relativePath(p.Path) {
			kept = append(kept, p)
		}
	}
	c.Paths = kept
	c.PathsTruncated = c.PathCount > len(kept)
	return c
}

// relativePath reports whether p reads as a path inside the repository:
// not empty, not absolute in any spelling, no URL, no ".." segment. git
// prints no other kind, but a path that is not one is never sent.
func relativePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || strings.Contains(p, "://") {
		return false
	}
	if len(p) >= 2 && p[1] == ':' && ('A' <= p[0] && p[0] <= 'Z' || 'a' <= p[0] && p[0] <= 'z') {
		return false
	}
	return !slices.Contains(strings.Split(p, "/"), "..")
}

// encodeJSON is compact JSON without the HTML escaping encoding/json
// applies by default, so a message reads in the preview as it was
// written.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}

func gzipSize(body []byte) (int, error) {
	var n countingWriter
	zw := gzip.NewWriter(&n)
	if _, err := zw.Write(body); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	return int(n), nil
}

type countingWriter int

func (c *countingWriter) Write(p []byte) (int, error) {
	*c += countingWriter(len(p))
	return len(p), nil
}
