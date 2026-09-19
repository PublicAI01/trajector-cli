package lifecycle

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
)

// HookInput is what Claude Code writes on a hook's stdin: which session
// is running, the file it records to, where it runs, which event it is,
// and — for a hook that runs after a tool — what the tool was given and
// what it answered. Every other key is ignored, and a hook handed no
// such input gets the zero value.
//
// The two tool members stay raw. Their shape is the tool's, not this
// client's, so a tool that answers with something other than an object
// must cost the caller nothing: decoding them into named fields here
// would make one such answer discard the whole input.
type HookInput struct {
	SessionID    string          `json:"session_id"`
	SessionPath  string          `json:"transcript_path"`
	Cwd          string          `json:"cwd"`
	HookEvent    string          `json:"hook_event_name"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
}

// Command is the shell command the tool was given, empty when the input
// named none.
func (in HookInput) Command() string {
	var v struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(in.ToolInput, &v) != nil {
		return ""
	}
	return v.Command
}

// ReportedCommit reports whether the tool's own answer stated that it
// made a commit. Only the presence of the statement is read: the short
// identifier it carries is not the one a record names, and reading it
// further would be a judgement about what the tool did.
func (in HookInput) ReportedCommit() bool {
	var v struct {
		GitOperation struct {
			Commit json.RawMessage `json:"commit"`
		} `json:"gitOperation"`
	}
	if json.Unmarshal(in.ToolResponse, &v) != nil {
		return false
	}
	return len(v.GitOperation.Commit) > 0 && !bytes.Equal(v.GitOperation.Commit, []byte("null"))
}

// maxHookInput bounds how much of stdin a hook reads. Hook input is a
// few hundred bytes of JSON; anything past this is not it.
const maxHookInput = 1 << 20

// ReadHookInput decodes hook input from r. Input that is empty or not a
// JSON object yields the zero value rather than an error: a hook runs
// to completion on whatever it is handed.
func ReadHookInput(r io.Reader) HookInput {
	data, err := io.ReadAll(io.LimitReader(r, maxHookInput))
	if err != nil {
		return HookInput{}
	}
	var in HookInput
	if err := json.Unmarshal(data, &in); err != nil {
		return HookInput{}
	}
	return in
}

// cloudPlaceholder is the file name Claude Code hands a hook it runs on
// this machine on behalf of a session that lives elsewhere. Nothing is
// ever written to it here.
const cloudPlaceholder = "cloud-transcript.jsonl"

// registerSessionFile registers the session file a hook was told about,
// and the agent files beside it, for reading on the project's behalf.
// It registers only when the hook ran inside an enabled project and the
// path names a file Claude Code writes on this machine: under the
// session files root, not the placeholder a cloud session hands a local
// hook, and not written by a Claude Code on the Windows side of a WSL
// boundary, whose files this process cannot reach.
// Anything else registers nothing and says nothing: the session must
// not learn what the hook did, and the registry is where the outcome
// is read afterwards. Registering is idempotent, so every hook of a
// session may call it.
func (m *Machine) registerSessionFile(cwd string, hook HookInput) (registered bool, err error) {
	path := hook.SessionPath
	if path == "" || claudesettings.WindowsSideClaude("", path) || !filepath.IsAbs(path) {
		return false, nil
	}
	path = filepath.Clean(path)
	if !discover.UnderSessionFiles(m.claude().ConfigDir, path) || filepath.Base(path) == cloudPlaceholder {
		return false, nil
	}
	st, err := m.Project(cwd)
	if err != nil {
		return false, err
	}
	if !st.Enabled || claudesettings.WindowsSideClaude(st.Root, "") {
		return false, nil
	}
	subpath := projectSubpath(st.Root, hook.Cwd)
	for _, p := range append([]string{path}, follow.Siblings(path)...) {
		if err := m.registry.RegisterUnder(st.Hash, p, subpath); err != nil {
			return false, err
		}
	}
	return true, nil
}

// followSession takes the session a hook was told about: it registers
// the session's file, marks it hot — or cold, when the session ended
// — and has it read. The resident process reads it on this hook's
// word when one is up; when none is, a one-shot reader is started for
// the project, and that reader brings the resident process up on its
// way out. A file that is not registered is nothing to read, and a
// failure of any step stays here: the session must not learn what
// the hook did, and the registry is where the outcome is read
// afterwards.
func (m *Machine) followSession(cwd string, hook HookInput, ended bool) {
	registered, err := m.registerSessionFile(cwd, hook)
	if err != nil || !registered {
		return
	}
	st, err := m.Project(cwd)
	if err != nil {
		return
	}
	path := filepath.Clean(hook.SessionPath)
	pid := sessionPID()
	if ended {
		_ = m.registry.Cool(st.Hash, path)
	} else {
		_ = m.registry.Warm(st.Hash, path, pid, m.deps.Now())
	}
	err = m.proxy.Progress(apiproxy.Progress{ProjectIDHash: st.Hash, Path: path, PID: pid, End: ended})
	if err != nil {
		_ = m.spawnReader(cwd)
	}
}

// sessionPID is the process the session runs in, as seen from a hook
// it started: the hook's parent. A shell between the two makes this
// the shell's id, which is gone by the next sweep; the file then stays
// hot on its events alone, and the registry never keeps a process it
// cannot see alive.
func sessionPID() int { return os.Getppid() }

// registryContents is one project's registry as everything here reads
// it: which files are registered, what the one search for earlier
// files could not cover, and what reading noticed about their shape.
type registryContents struct {
	Files   []follow.File
	Gaps    follow.Gaps
	Signals drift.Signals
	// Err, when non-nil, is why the registry could not be read. The
	// other fields are then zero.
	Err error
}

// sessionFiles reads one project's registry, and is the one place this
// package says what a registry that cannot be read means: no files and
// a stated reason. A surface then prints the reason rather than an
// empty registry, and a reading run does nothing rather than start
// every file from its beginning again. Withdrawal on disable is the
// exception and keeps its own failure: it changes the registry, so it
// must not report that it took back what it could not reach.
func (m *Machine) sessionFiles(projectIDHash string) registryContents {
	files, err := m.registry.Files(projectIDHash)
	if err != nil {
		return registryContents{Err: err}
	}
	gaps, err := m.registry.Gaps(projectIDHash)
	if err != nil {
		return registryContents{Err: err}
	}
	signals, err := m.registry.Signals(projectIDHash)
	if err != nil {
		return registryContents{Err: err}
	}
	return registryContents{Files: files, Gaps: gaps, Signals: signals}
}

// projectSubpath reports where a session ran relative to the project
// root, with forward slashes, and "" when it ran at the root itself.
// A session directory outside the root, or a working directory that
// cannot be resolved, has no position and yields "".
func projectSubpath(root, cwd string) string {
	subpath, _ := projectPosition(root, cwd)
	return subpath
}

// projectPosition reports where dir sits relative to the project root,
// with forward slashes and "" at the root itself, and false when dir is
// outside the root or cannot be resolved. The two answers are kept
// apart because "at the root" and "not in the project" are the same
// empty string and opposite decisions.
func projectPosition(root, dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	canonical, err := consent.CanonicalRoot(dir)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, canonical)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	return filepath.ToSlash(rel), true
}
