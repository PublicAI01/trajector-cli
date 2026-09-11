package lifecycle

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
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
	subpath := projectSubpath(st.Root, hook.Cwd)
	registry := follow.Open(m.deps.Layout.FollowDir())
	for _, p := range append([]string{path}, follow.Siblings(path)...) {
		if err := registry.RegisterUnder(st.Hash, p, subpath); err != nil {
			return false, err
		}
	}
	return true, nil
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

// ReadSessionFiles reads the files registered for a project once, on
// behalf of the session that just ran, and exits. It is the body of the
// detached process a session hook starts: for each registered file it
// consumes what the file gained since its cursor, lands the records in
// the spool, and advances the cursor only once every record is stored.
// It never blocks a session — the hook released it — and it says
// nothing: its streams are the null device, and a failure to read one
// file is left behind so the next file, and the next run, still make
// progress.
//
// A project that is not enabled, or whose injection was removed, is
// nothing to read: the injection shape is what the record must state
// about this client, so without one there is no record to write.
// Whenever there is a shape, the resident process is brought up on the
// way out: it is the one flusher, and it drains whatever the spool
// holds — the records just written, and any a previous run left behind
// because no flusher was up to send them.
func (m *Machine) ReadSessionFiles(projectDir string, io IO) error {
	st, err := m.Project(projectDir)
	if err != nil || !st.Enabled {
		return nil
	}
	injection, ok := injectionValue(claudesettings.InjectionShape(st.SettingsPath()))
	if !ok {
		return nil
	}
	defer func() { _ = m.EnsureProxy(projectDir, io) }()

	registry := follow.Open(m.deps.Layout.FollowDir())
	files, err := registry.Files(st.Hash)
	if err != nil {
		return nil
	}
	sp, err := m.spool()
	if err != nil {
		return nil
	}
	now := m.deps.Now().UTC().Format(time.RFC3339Nano)
	readAt := m.deps.Now().UTC().Format(time.RFC3339)

	for _, f := range files {
		capture := envelope.TranscriptCapture{
			ClientVersion:  m.deps.Version,
			Timestamp:      now,
			ProjectIDHash:  st.Hash,
			ProjectSubpath: f.Subpath,
			Injection:      injection,
		}
		res, err := follow.Read(f, capture, follow.ReadOptions{Root: st.Root})
		if err != nil {
			// A file that could not be read this time — a metadata file
			// caught mid-write, say — keeps its cursor and is tried again
			// next run. Reading the rest of the project goes on.
			continue
		}
		stop, err := m.inspectSegments(registry, st.Hash, res.Segments)
		if err != nil {
			continue
		}
		if stop {
			// Nothing of this read is stored and the cursor stays: the
			// same lines are met again by whichever build reads next,
			// and only one that can mask them may store them.
			return nil
		}
		full, err := storeRecords(sp, res)
		if full {
			// The spool is full: it dropped nothing, and neither does the
			// reader. The cursor stays where it was, so this file and
			// every one after it is read again once space returns; no
			// record is repeated, because storing is idempotent by id.
			return nil
		}
		if err != nil {
			continue
		}
		if res.Reaction == follow.Vanished || res.Stopped {
			_ = registry.Remove(st.Hash, f.Path)
			continue
		}
		// The cursor carries when it was last moved, so status can say
		// when a file was last read without a clock of its own.
		res.File.ReadAt = readAt
		_ = registry.Update(st.Hash, res.File)
	}
	return nil
}

// inspectSegments holds each segment's lines against the shape this
// build masks and reads by, before anything is stored, and keeps what
// it noticed with the project's registry and in the reader log. Lines
// this build cannot mask stop the run and pause recording device-wide
// until a different build reads them; everything else is counted and
// reading goes on. This is the one place a line's fields are read
// before the spool, so it is where the shape is checked; the reader
// itself interprets nothing.
func (m *Machine) inspectSegments(registry *follow.Registry, projectIDHash string, segments []envelope.Segment) (stop bool, err error) {
	for _, seg := range segments {
		found, err := drift.Scan([]byte(seg.Lines))
		if err != nil {
			return false, err
		}
		if !found.Any() {
			continue
		}
		_ = registry.AddSignals(projectIDHash, signalsOf(found))
		m.appendReaderLog(projectIDHash, found)
		if found.Stop() {
			_ = m.routes.PauseByBuild(routing.PauseRedactionDrift, m.deps.Version)
			return true, nil
		}
	}
	return false, nil
}

// signalsOf is one scan's findings in the registry's accumulating
// form.
func signalsOf(r drift.Report) follow.Signals {
	s := follow.Signals{
		UnanchoredPathFields:           r.UnanchoredPathFields,
		AssistantLines:                 r.AssistantLines,
		AssistantLinesWithoutMessageID: r.AssistantLinesWithoutMessageID,
		MessagesWithBlockIndexGap:      r.MessagesWithBlockIndexGap,
		AgentLines:                     r.AgentLines,
		AgentLinesWithoutParent:        r.AgentLinesWithoutParent,
		NewLaunchSurfaces:              r.NewLaunchSurfaces,
		NewAttachmentTypes:             r.NewAttachmentTypes,
		NewSystemSubtypes:              r.NewSystemSubtypes,
		NewTopLevelTypes:               r.NewTopLevelTypes,
	}
	if r.IncompleteLine {
		s.IncompleteSegments = 1
	}
	return s
}

// readerLogLine is one entry of the reader log: when, for which
// project, whether reading stopped, and what was found. It carries no
// path and no session id.
type readerLogLine struct {
	At            string `json:"at"`
	ProjectIDHash string `json:"project_id_hash"`
	Stop          bool   `json:"stop,omitempty"`
	follow.Signals
}

// appendReaderLog adds one line for a scan that found something. The
// reader has no other voice: its streams are the null device, and the
// registry keeps sums, not events. A log that cannot be written is
// let go — the registry still has the finding.
func (m *Machine) appendReaderLog(projectIDHash string, found drift.Report) {
	path := m.deps.Layout.ReaderLog()
	if err := userdirs.EnsureOwnerDir(filepath.Dir(path)); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, err := json.Marshal(readerLogLine{
		At:            m.now(),
		ProjectIDHash: projectIDHash,
		Stop:          found.Stop(),
		Signals:       signalsOf(found),
	})
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
}

// storeRecords writes a read result's segments and snapshots to the
// spool. full reports that the spool refused a record for want of room,
// the one outcome that must stop the whole run rather than advance a
// cursor past records that were never stored.
func storeRecords(sp *spool.Spool, res follow.ReadResult) (full bool, err error) {
	for _, seg := range res.Segments {
		if err := sp.WriteSegment(seg); err != nil {
			if errors.Is(err, spool.ErrQuotaExceeded) {
				return true, nil
			}
			return false, err
		}
	}
	for _, snap := range res.Snapshots {
		if err := sp.WriteMetaSnapshot(snap); err != nil {
			if errors.Is(err, spool.ErrQuotaExceeded) {
				return true, nil
			}
			return false, err
		}
	}
	return false, nil
}

// injectionValue names, for a record, what this client did with the
// project's traffic: forwarded it, or only read the files it left. An
// injection that is neither shape is not set up to record, and the
// second return is false.
func injectionValue(shape claudesettings.Shape, present bool) (string, bool) {
	if !present {
		return "", false
	}
	switch shape {
	case claudesettings.WithProxy:
		return envelope.InjectionProxy, true
	case claudesettings.WithoutProxy:
		return envelope.InjectionTailOnly, true
	}
	return "", false
}
