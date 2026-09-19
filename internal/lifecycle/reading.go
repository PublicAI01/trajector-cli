package lifecycle

import (
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
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
		Now:       m.deps.Now,
	}
}

// spawnReader starts a detached process that reads projectDir's
// registered files, and returns as soon as it is started. The hook that
// calls this is on the session's critical path, so reading happens in a
// process the session never waits for; the process inherits none of
// the hook's streams, which keeps the hook's own output empty.
func (m *Machine) spawnReader(projectDir string) error {
	_, err := proxylife.StartDetached(m.deps.ExecPath, []string{"hook", claudesettings.HookRead, projectDir}, "")
	return err
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
