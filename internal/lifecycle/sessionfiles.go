package lifecycle

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
)

// HookInput is what Claude Code writes on a hook's stdin: which session
// is running, the file it records to, and where. Every other key is
// ignored, and a hook handed no such input gets the zero value.
type HookInput struct {
	SessionID   string `json:"session_id"`
	SessionPath string `json:"transcript_path"`
	Cwd         string `json:"cwd"`
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

// RegisterSessionFile registers the session file a hook was told about,
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
func (m *Machine) RegisterSessionFile(cwd string, hook HookInput) (registered bool, err error) {
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

// FollowSession takes the session a hook was told about: it registers
// the session's file, and starts the reader for the project when that
// file is now registered. A file that is not registered is nothing to
// read, and a failure of either step stays here: the session must not
// learn what the hook did, and the registry is where the outcome is
// read afterwards.
func (m *Machine) FollowSession(cwd string, hook HookInput) {
	registered, err := m.RegisterSessionFile(cwd, hook)
	if err != nil || !registered {
		return
	}
	_ = m.SpawnReader(cwd)
}

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
	if cwd == "" {
		return ""
	}
	canonical, err := consent.CanonicalRoot(cwd)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(root, canonical)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}
