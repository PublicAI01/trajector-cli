package lifecycle

import (
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/sessionread"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// reader is the one rule for reading session files, bound to this
// device's stores and to the spool the caller opened for it.
func (m *Machine) reader(sp *spool.Spool) sessionread.Reader {
	return sessionread.Reader{
		Registry:  m.registry,
		Routes:    m.routes,
		Spool:     sp,
		Version:   m.deps.Version,
		ReaderLog: m.deps.Layout.ReaderLog(),
		Home:      m.deps.Home,
		Now:       m.deps.Now,
	}
}

// spawnReader starts the process that reads projectDir's registered
// files, and returns as soon as it is started. The hook that calls
// this is on the session's critical path, so reading happens in a
// process the session never waits for; the process inherits none of
// the hook's streams, which keeps the hook's own output empty. It
// goes through the same starter the proxy does, so one suite-wide
// choice covers both processes a hook can leave behind.
func (m *Machine) spawnReader(projectDir string) error {
	_, err := m.deps.Spawn(m.deps.ExecPath, []string{"hook", claudesettings.HookRead, projectDir}, "")
	return err
}

// readEarlierSessions has the session files a command just registered
// read without waiting for a session hook to name one of them. The
// resident process reads a file on a hook's word and, on its own,
// looks only at the files of sessions that are running; a file
// registered from outside a session belongs to neither set, so the
// command that registered it is what asks for it to be read.
//
// The two shapes of the ask are the two the hook path already has: a
// resident process is told about each session by name, and where none
// is up a one-shot reader is started for the project, which reads
// every registered file and brings the resident process up on its way
// out. One failed report is enough to fall back — the reader covers
// every file, so reporting the rest after it would read them twice.
// Each report names a session's own file and asks for it to be read
// to its end, so the agent files beside it are read with it.
//
// Nothing here waits for the reading to finish, and the error means
// only that no reader took the ask: both shapes refused, which is the
// device where no resident process is up and none can be started. The
// sweep that looks at what a registration just marked runs inside
// that same resident process, so it is no fallback here. The fallback
// is the next session of this project: its hook names the session, and
// the reading starts from where the entry stands. The files are
// registered either way, so the caller states this rather than fails
// on it.
func (m *Machine) readEarlierSessions(st report.ProjectStatus, sessions []string) error {
	for _, path := range sessions {
		if err := m.proxy.Progress(apiproxy.Progress{ProjectIDHash: st.Hash, Path: path, End: true}); err != nil {
			if spawnErr := m.spawnReader(st.Root); spawnErr != nil {
				return fmt.Errorf("the resident process did not take the report (%v) and no reader could be started: %w", err, spawnErr)
			}
			return nil
		}
	}
	return nil
}

// ReadSessionFiles reads the files registered for a project once, on
// behalf of the session that just ran, and exits. It is the body of the
// detached process a session hook starts when no resident process is
// up to read on the hook's word: it hands every registered entry of
// the project to one reader, and what the reader consumes is stored
// in the spool. It never blocks a session — the hook released it — and
// it says nothing: its streams are the null device, it reports no
// outcome, and a failure to read one file is left behind so the next
// file, and the next run, still make progress.
//
// A project that is not enabled, or whose injection was removed, is
// nothing to read: no injection stands, so no session of Claude Code's
// ran under one. Past that the resident process is brought up on the
// way out, whatever the run itself does: it is the one flusher, and it
// drains whatever the spool holds — the records just written, and any
// a previous run left behind because no flusher was up to send them.
//
// A project the routing table does not clear is nothing to read
// either: the reader asks the table the same question the proxy asks
// before it records, so a device-wide pause stops every recording
// path and not only the one the proxy is on. It stops neither
// forwarding nor the flusher, which is why it leaves the way out
// alone. What each record states about this client is the shape the
// grant records, never a reading of the settings file, so a
// hand-edited file cannot make two runs disagree about one project.
func (m *Machine) ReadSessionFiles(projectDir string, io IO) {
	st, err := m.Project(projectDir)
	if err != nil || !st.Enabled || !st.Injected {
		return
	}
	defer func() { _ = m.EnsureProxy(projectDir, io) }()

	sp, err := m.spool()
	if err != nil {
		return
	}
	project := sessionread.Project{Token: st.Token, Hash: st.Hash, Root: st.Root, Shape: st.Shape}
	m.reader(sp).Read(project, m.sessionFiles(st.Hash).Files)
}

// injectionValue names, for a record, what this client did with the
// project's traffic: forwarded it, or only read the files it left. It
// is the wire spelling of the shape the grant records.
func injectionValue(shape routing.Shape) string {
	return sessionread.InjectionValue(shape)
}
