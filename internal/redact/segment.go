package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/sessionline"
)

// ErrIncompleteLine is returned when a segment's last line does not end
// in a newline. A segment holds complete lines only: a cut line is not
// JSON, so the field-aware pass would fall back to a full-text scan and
// rewrite what the field policy exists to keep. Nothing is guessed or
// trimmed here; the caller keeps the cut line for the next segment.
var ErrIncompleteLine = errors.New("redact: segment does not end in a newline")

// labelPath is the label of the replacement token a stripped
// project-locating value gets.
const labelPath = "PATH"

// labelKey is the label of the replacement token a key that is not a
// plain field name gets when a key path is made printable.
const labelKey = "KEY"

// keyPath addresses one string value in a line by object keys alone,
// from the line's root down. A value under an array is never addressed
// by a keyPath.
type keyPath []string

// printable returns the name a surface may show for this key path. A
// key is data as much as a value is: a session line can hold a file of
// the user's in key position, so a name built from keys alone can
// carry a path that never went through masking. Every key that is not
// a plain field name is replaced by the [REDACTED_KEY] token here, at
// the one place a name is made. The result holds no key that this
// package did not first prove printable.
func (p keyPath) printable() string {
	keys := make([]string, len(p))
	for i, key := range p {
		if isPlainFieldName(key) {
			keys[i] = key
			continue
		}
		keys[i] = replacementToken(labelKey)
	}
	return "$." + strings.Join(keys, ".")
}

// isPlainFieldName reports whether a key is spelled with ASCII letters,
// digits, "_" and "-" only. A path always needs a separator, a drive
// colon, or a space, and this set holds none of them, so a key that
// passes names no location. The set also keeps "." out of a key, which
// lets the printed name be read back as the address it states.
func isPlainFieldName(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func (p keyPath) equal(q []string) bool {
	if len(p) != len(q) {
		return false
	}
	for i := range p {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// valueGuard is a condition on another string value in the same line:
// it holds when some value at path equals want.
type valueGuard struct {
	path keyPath
	want string
}

// pathField names one string field of a line whose whole value is an
// absolute path by construction, listed so the field is handled by
// its position rather than by the shape of its value.
type pathField struct {
	path keyPath
	// guards, when set, must all hold for the entry to apply to a line.
	guards []valueGuard
}

// guardTracker records, while one line is walked, which guards of each
// listed entry have been seen to hold. An entry with no guards holds
// unconditionally.
type guardTracker [][]bool

func newGuardTracker(entries []pathField) guardTracker {
	t := make(guardTracker, len(entries))
	for i, e := range entries {
		t[i] = make([]bool, len(e.guards))
	}
	return t
}

// observe marks every guard that this string value satisfies.
func (t guardTracker) observe(entries []pathField, path []string, value string) {
	for i, e := range entries {
		for j, g := range e.guards {
			if g.path.equal(path) && value == g.want {
				t[i][j] = true
			}
		}
	}
}

// held reports whether every guard of entry i has been seen to hold.
func (t guardTracker) held(i int) bool {
	return !slices.Contains(t[i], false)
}

// anchoredPaths lists the fields whose value is, by definition, a copy
// of where the session ran: the project directory, or an encoding of
// it. Each is replaced by the [REDACTED_PATH] token, key kept, before
// the line is scanned for secrets. Matching is by position from the
// line's root, never by key name, so a same-named key in a tool result
// or in message text is an observed value and stays as it was.
//
// A field goes on this list only when its value is, by construction,
// the project's location or derived from it. A tool's file path, a request's file_path, an
// attachment's filename can all name a file elsewhere, and masking
// them would rewrite an observation; they are not here.
var anchoredPaths = []pathField{
	{path: keyPath{"cwd"}},
	{
		path:   keyPath{"attachment", "snapshot", "scratchpadDirectory"},
		guards: []valueGuard{{path: keyPath{"attachment", "type"}, want: "environment"}},
	},
}

// acknowledgedPaths lists the fields AbsolutePathFields has already
// seen and that are deliberately left as observed: the same value
// stands as text elsewhere in the session, on the same line or in the
// line it was copied from, so masking the structured copy alone would
// hide nothing.
var acknowledgedPaths = []pathField{
	{path: keyPath{"attachment", "snapshot", "workingDirectory"}},
	// lastPrompt is the text the user typed last, which Claude Code
	// keeps for resuming. A slash command or a bare path typed as a
	// prompt has the shape of a path, but the value is what was typed,
	// not where the session ran, and the same text stands in the user
	// line it came from.
	{
		path:   keyPath{"lastPrompt"},
		guards: []valueGuard{{path: keyPath{"type"}, want: "last-prompt"}},
	},
	// content on a queue-operation line is a prompt the user typed
	// while a turn was running, kept until it is sent. A one-word
	// slash command such as /exit has the shape of a path, but the
	// value is what was typed, not where the session ran, and the same
	// text stands in the user line it is sent as.
	{
		path:   keyPath{"content"},
		guards: []valueGuard{{path: keyPath{"type"}, want: "queue-operation"}},
	},
	// content on a system/local_command line is the slash command the
	// user typed, echoed back with the client's answer to it. It is
	// the same typed text, for the same reason; no other system
	// subtype carries a top-level content that names a location.
	{
		path: keyPath{"content"},
		guards: []valueGuard{
			{path: keyPath{"type"}, want: "system"},
			{path: keyPath{"subtype"}, want: "local_command"},
		},
	},
}

// RedactSegment masks one segment for upload: the anchored project
// paths are stripped first, then the raw JSONL bytes go through the
// same pass every rawcall goes through. The envelope around the lines
// (session_id, file, segment_index, capture) is not scanned: every one
// of those is a value this client wrote itself, and a scan could only
// damage it.
//
// The returned segment is redacted as a whole. The caller serializes it
// and wraps the bytes with AlreadyRedacted; a second pass over the
// serialized record would see the lines as one string value and undo
// the field policy that kept signatures and ids intact.
//
// Lines must end in a newline, or ErrIncompleteLine is returned.
func RedactSegment(seg envelope.Segment) (envelope.Segment, error) {
	if !strings.HasSuffix(seg.Lines, "\n") {
		return envelope.Segment{}, ErrIncompleteLine
	}
	var stripped strings.Builder
	stripped.Grow(len(seg.Lines))
	rest := []byte(seg.Lines)
	for len(rest) > 0 {
		nl := bytes.IndexByte(rest, '\n')
		line := rest[:nl]
		rest = rest[nl+1:]
		// A line that is not one of a session file names no anchored
		// field: its bytes go on as they are, and the pass below is
		// what masks them.
		if parsed, ok := sessionline.Parse(line); ok {
			stripped.WriteString(stripAnchoredPaths(parsed))
		} else {
			stripped.Write(line)
		}
		stripped.WriteByte('\n')
	}
	redacted, err := JSONLBytes([]byte(stripped.String()))
	if err != nil {
		return envelope.Segment{}, err
	}
	seg.Lines = string(redacted.Bytes())
	return seg, nil
}

// RedactMetaSnapshot masks one snapshot for upload. Content is a JSON
// object and goes through the field-aware pass as a single document;
// the envelope around it is not scanned. The record id names the
// content, so it is computed again from the masked content: what the
// id names must be what the record carries.
//
// The returned snapshot is redacted as a whole; the caller wraps its
// serialized bytes with AlreadyRedacted.
func RedactMetaSnapshot(snap envelope.MetaSnapshot) (envelope.MetaSnapshot, error) {
	redacted, err := JSONLBytes(snap.Content)
	if err != nil {
		return envelope.MetaSnapshot{}, err
	}
	content := append([]byte(nil), redacted.Bytes()...)
	id, err := envelope.MetaSnapshotRecordID(snap.SessionID, snap.File, content)
	if err != nil {
		return envelope.MetaSnapshot{}, err
	}
	snap.RecordID = id
	snap.Content = json.RawMessage(content)
	return snap, nil
}

// stripAnchoredPaths replaces the value of every anchored field in one
// line with the [REDACTED_PATH] token and leaves every other byte as it
// was. The string tokens are located by walking the raw text, not by
// decoding and re-encoding the line: a round trip would reorder keys,
// escape HTML, and reformat numbers, and any of those breaks a
// signature carried on the same line. Hits arrive in text order, so the
// rebuild needs no sorting.
func stripAnchoredPaths(parsed sessionline.Line) string {
	line := parsed.Text()
	type hit struct {
		entry int
		region
	}
	var hits []hit
	guards := newGuardTracker(anchoredPaths)
	walkStringValues(line, func(path []string, start, end int, value string) {
		for i, a := range anchoredPaths {
			if a.path.equal(path) {
				hits = append(hits, hit{entry: i, region: region{start, end}})
			}
		}
		guards.observe(anchoredPaths, path, value)
	})
	var regions []region
	for _, h := range hits {
		if guards.held(h.entry) {
			regions = append(regions, h.region)
		}
	}
	if len(regions) == 0 {
		return line
	}
	token := `"` + replacementToken(labelPath) + `"`
	var b strings.Builder
	b.Grow(len(line))
	prev := 0
	for _, r := range regions {
		b.WriteString(line[prev:r.start])
		b.WriteString(token)
		prev = r.end
	}
	b.WriteString(line[prev:])
	return b.String()
}

// SessionLocation is where the session whose lines are read lives: the
// user's home directory — the user profile directory on Windows — and
// the project directory the reader was given. Both are the caller's to
// state, because nothing in this package reads the environment: a
// masking rule that changed with the machine it ran on could not be
// tested and could not be reasoned about.
type SessionLocation struct {
	Home    string
	Project string
}

// AbsolutePathFields lists the JSON paths of every string value in the
// line that names where the session ran and that neither the anchored
// list nor the acknowledged list knows. A non-empty result means a
// field appeared that neither list knows and that this build would
// therefore let through unmasked.
//
// What comes back is printable as it stands: a name holds field names
// and replacement tokens and nothing else, so no path of the user's
// can leave through a name even when the line puts its files in key
// position. No value from the line is ever part of a name. Each name
// appears once.
//
// Only two layers are looked at: the line's top-level fields, and the
// fields directly under attachment.snapshot. Deeper values — message
// content, tool inputs, tool results — are observations that may name
// files anywhere, and are never candidates for anchoring. A value under
// an array is not looked at either.
//
// A value names where the session ran when it is an absolute path —
// it starts with "/" and holds no whitespace, or has the form of a
// Windows drive root such as "C:\" or "C:/" — and it is one of:
// inside the user's home directory, inside the project directory
// (loc.Project, or the anchored cwd value the line itself carries), or
// equal to an anchored field's value. Any other absolute path is a
// value the session observed, not its own location: a typed slash
// command, a system directory, a file elsewhere on the machine. Those
// are left to the masking pass, which is what the shape of a value is
// for.
func AbsolutePathFields(line sessionline.Line, loc SessionLocation) []string {
	s := line.Text()
	listed := append(append([]pathField{}, anchoredPaths...), acknowledgedPaths...)
	guards := newGuardTracker(listed)
	var candidates []pathValue
	var anchored []anchoredValue
	walkStringValues(s, func(path []string, start, end int, value string) {
		guards.observe(listed, path, value)
		for i, a := range anchoredPaths {
			if a.path.equal(path) {
				anchored = append(anchored, anchoredValue{entry: i, value: value})
			}
		}
		if isProbedLayer(path) && isAbsolutePath(value) {
			candidates = append(candidates, pathValue{path: append(keyPath(nil), path...), value: value})
		}
	})
	// The whole line is walked before a candidate is judged: cwd may
	// stand after the field that copies it, and a guard that settles an
	// anchored field may stand after both.
	where := sessionLocation{home: loc.Home, dirs: []string{loc.Project}}
	for _, a := range anchored {
		if !guards.held(a.entry) {
			continue
		}
		where.values = append(where.values, a.value)
		if anchoredPaths[a.entry].path.equal([]string{"cwd"}) {
			where.dirs = append(where.dirs, a.value)
		}
	}
	var found []string
	seen := make(map[string]bool)
	for _, c := range candidates {
		known := false
		for i, a := range listed {
			if a.path.equal(c.path) && guards.held(i) {
				known = true
			}
		}
		if known || !where.holds(c.value) {
			continue
		}
		name := c.path.printable()
		if seen[name] {
			continue
		}
		seen[name] = true
		found = append(found, name)
	}
	return found
}

// pathValue is one candidate: where it stands in the line, and what
// it says.
type pathValue struct {
	path  keyPath
	value string
}

// anchoredValue is one anchored field's value as a line carried it,
// kept with the entry that listed the field so a guard settled later
// in the line still decides whether the value counts.
type anchoredValue struct {
	entry int
	value string
}

// sessionLocation answers the one question the report turns on: does
// this value name where the session ran?
type sessionLocation struct {
	home string
	// dirs are the directories a value inside them belongs to the
	// session's own location.
	dirs []string
	// values are the anchored values the line carried, which a
	// candidate equal to one of them copies.
	values []string
}

func (w sessionLocation) holds(value string) bool {
	if value == "" {
		return false
	}
	if slices.Contains(w.values, value) {
		return true
	}
	if underDir(value, w.home) {
		return true
	}
	for _, dir := range w.dirs {
		if underDir(value, dir) {
			return true
		}
	}
	return false
}

// underDir reports whether value is dir or lies inside it. Separators
// are read the same either way and a Windows path is compared without
// case, because a session file may spell the same directory either way
// and a location missed is a location uploaded.
func underDir(value, dir string) bool {
	if dir == "" {
		return false
	}
	v, d := comparablePath(value), comparablePath(dir)
	d = strings.TrimSuffix(d, "/")
	return v == d || strings.HasPrefix(v, d+"/")
}

func comparablePath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if hasDriveRoot(p) {
		return strings.ToLower(p)
	}
	return p
}

func hasDriveRoot(p string) bool {
	return len(p) >= 2 && p[1] == ':' &&
		((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}

func isProbedLayer(path []string) bool {
	if len(path) == 1 {
		return true
	}
	return len(path) == 3 && path[0] == "attachment" && path[1] == "snapshot"
}

func isAbsolutePath(v string) bool {
	if v == "" || strings.ContainsAny(v, " \t\r\n") {
		return false
	}
	if v[0] == '/' {
		return true
	}
	isDrive := len(v) >= 3 && v[1] == ':' && (v[2] == '\\' || v[2] == '/') &&
		((v[0] >= 'A' && v[0] <= 'Z') || (v[0] >= 'a' && v[0] <= 'z'))
	return isDrive
}

// walkStringValues calls visit for every string value in s that is
// addressed by object keys alone, with the value's key path from the
// root, the byte range of its token (quotes included), and its decoded
// value. Keys are decoded before they are compared, so a key spelled
// with escapes still addresses the same field. Values inside arrays,
// and a bare top-level string, are not visited. s must be valid JSON.
func walkStringValues(s string, visit func(path []string, start, end int, value string)) {
	type frame struct {
		isObject bool
		key      string
	}
	var stack []frame
	path := make([]string, 0, 8)
	i := 0
	for i < len(s) {
		switch s[i] {
		case '{':
			stack = append(stack, frame{isObject: true})
			i++
		case '[':
			stack = append(stack, frame{})
			i++
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			i++
		case '"':
			end, ok := jsonStringEnd(s, i)
			if !ok {
				return
			}
			value, decoded := decodeJSONStringToken(s[i:end])
			inObject := len(stack) > 0 && stack[len(stack)-1].isObject
			isKey := false
			if p := skipJSONWhitespace(s, end); p < len(s) && s[p] == ':' {
				isKey = true
			}
			switch {
			case !decoded || !inObject:
			case isKey:
				stack[len(stack)-1].key = value
			default:
				path = path[:0]
				addressable := true
				for _, f := range stack {
					if !f.isObject {
						addressable = false
						break
					}
					path = append(path, f.key)
				}
				if addressable {
					visit(path, i, end, value)
				}
			}
			i = end
		default:
			i++
		}
	}
}

// RedactGitSnapshot masks one git snapshot for upload. The branch name
// and the observed paths are text the user wrote, so both go through
// the pass, and through the same one, so a secret is masked the same
// way wherever the record carries it. Every other field — the commit
// and blob identifiers, the counts — is this client's own value or a
// forty-character identifier, and running an entropy-sensitive pass
// over those could only damage them.
//
// The returned snapshot is redacted as a whole; the caller wraps its
// serialized bytes with AlreadyRedacted.
func RedactGitSnapshot(snap envelope.GitSnapshot) (envelope.GitSnapshot, error) {
	// The branch leads, so one pass covers every user-written string
	// the record holds and the reply can be read back by position.
	written := make([]string, 0, len(snap.Changed)+1)
	written = append(written, snap.Branch)
	for _, c := range snap.Changed {
		written = append(written, c.Path)
	}
	encoded, err := json.Marshal(written)
	if err != nil {
		return envelope.GitSnapshot{}, err
	}
	redacted, err := JSONLBytes(encoded)
	if err != nil {
		return envelope.GitSnapshot{}, err
	}
	var masked []string
	if err := json.Unmarshal(redacted.Bytes(), &masked); err != nil {
		return envelope.GitSnapshot{}, err
	}
	if len(masked) != len(written) {
		return envelope.GitSnapshot{}, errors.New("redact: masking changed how many strings a git snapshot carries")
	}
	snap.Branch = masked[0]
	changed := slices.Clone(snap.Changed)
	for i := range changed {
		changed[i].Path = masked[i+1]
	}
	snap.Changed = changed
	return snap, nil
}
