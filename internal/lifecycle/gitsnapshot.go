package lifecycle

import (
	"context"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/gitsnapshot"
	"github.com/PublicAI01/trajector-cli/internal/report"
)

// ObserveGitSnapshot records what the repository the hook ran in looks
// like at this moment: which commit is checked out, which branch names
// it, and which paths differ from the commit it is compared against. No
// file content is read.
//
// It is called from hooks on a session's critical path, so it says
// nothing, reports nothing, and fails silently: every step that cannot
// be completed ends the observation, and the session is never told. The
// commands it runs are bounded by the observer's own deadline.
//
// Nothing is observed unless the hook ran inside an enabled project
// whose recording the routing table still clears — the same question
// the proxy asks before it records, so one device-wide pause stops
// every recording path. A directory outside the project, one that is
// not inside a repository, and a repository with no commit yet are all
// the same answer: there is nothing to observe.
func (m *Machine) ObserveGitSnapshot(cwd string, hook HookInput) {
	// Asked before anything is opened or run: the hook after a shell
	// tool use runs on every command, and almost none of them can have
	// made a commit.
	why, ok := gitsnapshot.ObserveReason(hook.HookEvent, hook.Command(), hook.ReportedCommit())
	if !ok {
		return
	}
	st, err := m.Project(cwd)
	if err != nil || !st.Enabled {
		return
	}
	if !m.recordingCleared(st.Token) {
		return
	}
	dir, subpath, ok := m.observedDir(st, hook)
	if !ok {
		return
	}

	observer := gitsnapshot.Observer{Dir: dir}
	position, err := observer.Position(context.Background())
	if err != nil {
		return
	}
	lastSeen, _ := m.heads.Last(st.Hash)
	base, ok := why.Compare(position, lastSeen)
	if !ok {
		return
	}

	var changed []envelope.Change
	if base != "" {
		if changed, err = observer.Changes(context.Background(), base, position.Head); err != nil {
			return
		}
	}
	now := m.deps.Now()
	snapshot := envelope.NewGitSnapshot(
		hook.SessionID, hook.HookEvent, why.Trigger,
		envelope.Capture{
			ClientVersion:  m.deps.Version,
			Timestamp:      now.UTC().Format(time.RFC3339Nano),
			ProjectIDHash:  st.Hash,
			ProjectSubpath: subpath,
			Injection:      injectionValue(st.Shape),
		},
		position.Branch, position.Head,
		envelope.CommitOrNone(position.Parent), envelope.CommitOrNone(base),
		changed,
	)

	sp, err := m.spool()
	if err != nil {
		return
	}
	if err := sp.WriteGitSnapshot(snapshot); err != nil {
		return
	}
	// The commit is remembered only once its observation is stored, so a
	// commit whose record was never written is still compared against
	// when the session ends.
	_ = m.heads.See(st.Hash, position.Head)
}

// observedDir is the directory to observe and where it sits inside the
// project. The session states its own working directory, which is where
// a linked worktree is entered; the directory the hook process runs in
// answers when the session states none. Either way the directory must
// lie inside the enabled project: consent is addressed by project, and
// a repository outside one was never granted.
func (m *Machine) observedDir(st report.ProjectStatus, hook HookInput) (dir, subpath string, ok bool) {
	if hook.Cwd == "" {
		return st.Root, "", true
	}
	if subpath, ok = projectPosition(st.Root, hook.Cwd); !ok {
		return "", "", false
	}
	return hook.Cwd, subpath, true
}
