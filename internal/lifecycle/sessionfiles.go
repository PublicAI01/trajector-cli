package lifecycle

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
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

// sessionFilesRoot is the directory Claude Code keeps session files
// under: projects/ inside its configuration directory, which
// CLAUDE_CONFIG_DIR relocates.
func (m *Machine) sessionFilesRoot() string {
	dir := m.deps.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(m.deps.Home, ".claude")
	}
	return filepath.Join(dir, "projects")
}

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
	if !under(m.sessionFilesRoot(), path) || filepath.Base(path) == cloudPlaceholder {
		return false, nil
	}
	st, err := m.Project(cwd)
	if err != nil {
		return false, err
	}
	if !st.Enabled || claudesettings.WindowsSideClaude(st.Root, "") {
		return false, nil
	}
	registry := follow.Open(m.deps.Layout.FollowDir())
	for _, p := range append([]string{path}, follow.Siblings(path)...) {
		if err := registry.Register(st.Hash, p); err != nil {
			return false, err
		}
	}
	return true, nil
}

// under reports whether path lies strictly inside dir, by name alone.
func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// readerSubcommand is the hook subcommand that reads a project's
// registered files.
const readerSubcommand = "read"

// SpawnReader starts a detached process that reads projectDir's
// registered files, and returns as soon as it is started. The hook that
// calls this is on the session's critical path, so reading happens in a
// process the session never waits for; the process inherits none of
// the hook's streams, which keeps the hook's own output empty.
func (m *Machine) SpawnReader(projectDir string) error {
	_, err := proxylife.StartDetached(m.deps.ExecPath, []string{"hook", readerSubcommand, projectDir}, "")
	return err
}

func (m *Machine) ReadSessionFiles(projectDir string, io IO) error {
	return nil
}
