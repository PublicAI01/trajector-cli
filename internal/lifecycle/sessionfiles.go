package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
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
	if err := m.registry.RegisterAll(st.Hash, append([]string{path}, follow.Siblings(path)...), subpath); err != nil {
		return false, err
	}
	return true, nil
}

// followSession takes the session a hook was told about: it registers
// the session's file, marks the session hot — or cold, when it ended
// — and has it read. The marking comes before the report and not
// after it: the resident process reads the heat from the registry,
// not from the report, so a report that arrived first would be acted
// on against what the last hook left. The resident process reads the
// session on this hook's word when one is up; when none is, a
// one-shot reader is started for the project, and that reader brings
// the resident process up on its way out. A file that is not
// registered is nothing to read, and a failure of any step stays
// here: the session must not learn what the hook did, and the
// registry is where the outcome is read afterwards.
//
// A project whose registry is waiting for its migration is not marked,
// and its file is kept as a pending registration: the hook leaves the
// migration to the one-shot reader.
func (m *Machine) followSession(ctx context.Context, cwd string, hook HookInput, ended bool) {
	registered, err := m.registerSessionFile(cwd, hook)
	if m.leaveMigrationToReader(ctx, cwd, err) || err != nil || !registered {
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
	err = m.proxy.Progress(ctx, apiproxy.Progress{ProjectIDHash: st.Hash, Path: path, End: ended})
	if err != nil {
		_ = m.spawnReader(ctx, cwd)
	}
}

// leaveMigrationToReader starts the one-shot reader for the project at
// dir when err says the project's registry waits for its migration, and
// reports whether it did. It is for a caller that may not wait for a
// migration: the reader migrates the project and reads it.
func (m *Machine) leaveMigrationToReader(ctx context.Context, dir string, err error) bool {
	if !errors.Is(err, follow.ErrMigrationPending) {
		return false
	}
	_ = m.spawnReader(ctx, dir)
	return true
}

// sessionPID is the process the session runs in, as seen from a hook
// it started: the hook's parent. A shell between the two makes this
// the shell's id, which is gone by the next sweep; the file then stays
// hot on its events alone, and the registry never keeps a process it
// cannot see alive.
func sessionPID() int { return os.Getppid() }

// registryContents is one project's registry as everything here reads
// it, from one reading of it.
type registryContents struct {
	follow.Contents
	// Err, when non-nil, is why the registry could not be read. The
	// contents are then empty.
	Err error
}

// registryMigration says whether a reading of a project's registry
// migrates a registry that waits for its migration, or reads it as it
// stands, as one that cannot be read until then.
type registryMigration bool

const (
	// migrateFirst is for status, doctor, enable and the one-shot
	// reader.
	migrateFirst registryMigration = true
	// asItStands is for the diagnostic bundle, which changes nothing it
	// diagnoses.
	asItStands registryMigration = false
)

// sessionFiles reads one project's registry, and is the one place this
// package says what a registry that cannot be read means: no files and
// a stated reason. A surface then prints the reason rather than an
// empty registry, and a reading run does nothing rather than start
// every file from its beginning again. Withdrawal on disable is the
// exception and keeps its own failure: it changes the registry, so it
// must not report that it took back what it could not reach.
//
// migration says whether the registry is migrated before it is read.
// When it is, a registry another process is migrating is read as one
// that cannot be read, this time.
func (m *Machine) sessionFiles(projectIDHash string, migration registryMigration) registryContents {
	if migration == migrateFirst {
		if err := m.registry.Migrate(projectIDHash); err != nil {
			return registryContents{Err: err}
		}
	}
	c, err := m.registry.Contents(projectIDHash)
	if err != nil {
		return registryContents{Err: err}
	}
	return registryContents{Contents: c}
}

// projectSubpath reports where a session ran relative to the project
// root, with forward slashes, and "" when it ran at the root itself.
// A session directory outside the root, or a working directory that
// cannot be resolved, has no position and yields "".
func projectSubpath(root, cwd string) string {
	subpath, _ := consent.ProjectPosition(root, cwd)
	return subpath
}
