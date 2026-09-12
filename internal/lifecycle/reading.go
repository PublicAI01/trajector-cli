package lifecycle

import (
	"errors"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// SpawnReader starts a detached process that reads projectDir's
// registered files, and returns as soon as it is started. The hook that
// calls this is on the session's critical path, so reading happens in a
// process the session never waits for; the process inherits none of
// the hook's streams, which keeps the hook's own output empty.
func (m *Machine) SpawnReader(projectDir string) error {
	_, err := proxylife.StartDetached(m.deps.ExecPath, []string{"hook", claudesettings.HookRead, projectDir}, "")
	return err
}

// ReadSessionFiles reads the files registered for a project once, on
// behalf of the session that just ran, and exits. It is the body of the
// detached process a session hook starts: it hands every registered
// entry of the project to one reader, and what the reader consumes is
// stored in the spool. Which entries are read, and what storing means,
// are decided here; when a cursor moves and what becomes of an entry
// are the reader's alone. It never blocks a session — the hook
// released it — and it says nothing: its streams are the null device,
// it reports no outcome, and a failure to read one file is left behind
// so the next file, and the next run, still make progress.
//
// A project that is not enabled, or whose injection was removed, is
// nothing to read: no injection stands, so no session of Claude Code's
// ran under one. Past that the resident process is brought up on the
// way out, whatever the run itself does: it is the one flusher, and it
// drains whatever the spool holds — the records just written, and any
// a previous run left behind because no flusher was up to send them.
//
// A project the routing table does not clear is nothing to read
// either: this asks the table the same question the proxy asks before
// it records, so a device-wide pause stops both recording paths and
// not only the one the proxy is on. It stops neither forwarding nor
// the flusher, which is why it leaves the way out alone. What each
// record states about this client is the shape the grant records,
// never a reading of the settings file, so a hand-edited file cannot
// make two runs disagree about one project.
func (m *Machine) ReadSessionFiles(projectDir string, io IO) {
	st, err := m.Project(projectDir)
	if err != nil || !st.Enabled || !st.Injected {
		return
	}
	defer func() { _ = m.EnsureProxy(projectDir, io) }()

	verdict, err := m.routes.Resolve(st.Token)
	if err != nil || !verdict.Records() {
		return
	}

	sp, err := m.spool()
	if err != nil {
		return
	}
	now := m.deps.Now()
	reader := follow.Reader{
		Registry:      m.registry,
		ProjectIDHash: st.Hash,
		Options:       follow.ReadOptions{Root: st.Root},
		Capture: envelope.TranscriptCapture{
			ClientVersion: m.deps.Version,
			Timestamp:     now.UTC().Format(time.RFC3339Nano),
			ProjectIDHash: st.Hash,
			Injection:     injectionValue(st.Shape),
		},
		Store:  func(res follow.ReadResult) follow.Storing { return m.storeRead(st.Hash, sp, res) },
		ReadAt: now,
	}
	for _, f := range m.sessionFiles(st.Hash).Files {
		if !reader.Advance(f) {
			return
		}
	}
}

// storeRead holds what one read produced against the shape this build
// masks and reads by, and then lands its records in the spool. It is
// the whole of what storing means here, and the reader moves a cursor
// only on the answer it gives.
func (m *Machine) storeRead(projectIDHash string, sp *spool.Spool, res follow.ReadResult) follow.Storing {
	hold, err := m.inspectSegments(projectIDHash, res.Segments)
	if err != nil {
		return follow.NotStored
	}
	if hold {
		// Nothing of this read is stored: the same lines are met again
		// by whichever build reads next, and only one that can mask
		// them may store them.
		return follow.Held
	}
	full, err := storeRecords(sp, res)
	switch {
	case full:
		// The spool is full: it dropped nothing, and neither does the
		// reader. This file and every one after it is read again once
		// space returns; no record is repeated, because storing is
		// idempotent by id.
		return follow.Held
	case err != nil:
		return follow.NotStored
	}
	return follow.Stored
}

// inspectSegments holds each segment's lines against the shape this
// build masks and reads by, before anything is stored, and keeps what
// it noticed with the project's registry and in the reader log. Lines
// this build cannot mask hold the run and pause recording device-wide
// until a different build reads them; everything else is counted and
// reading goes on. This is the one place a line's fields are read
// before the spool, so it is where the shape is checked; the reader
// itself interprets nothing, and what a finding is called is stated
// where the scan happens, not here. A registry or a log that cannot be
// written is let go: what was noticed is worth keeping and never worth
// stopping a read for.
func (m *Machine) inspectSegments(projectIDHash string, segments []envelope.Segment) (hold bool, err error) {
	for _, seg := range segments {
		found, err := drift.Scan([]byte(seg.Lines))
		if err != nil {
			return false, err
		}
		if !found.Any() {
			continue
		}
		_ = m.registry.AddSignals(projectIDHash, found)
		_ = drift.AppendLog(m.deps.Layout.ReaderLog(), m.now(), projectIDHash, found)
		if found.Stop() {
			_ = m.routes.PauseByBuild(routing.PauseRedactionDrift, m.deps.Version)
			return true, nil
		}
	}
	return false, nil
}

// storeRecords writes a read result's records to the spool. full
// reports that the spool refused a record for want of room, the one
// outcome that must stop the whole run rather than advance a cursor
// past records that were never stored. One pass answers for every
// record a read produced, so that outcome is decided once and not once
// per kind.
func storeRecords(sp *spool.Spool, res follow.ReadResult) (full bool, err error) {
	for _, write := range recordWrites(sp, res) {
		if err := write(); err != nil {
			if errors.Is(err, spool.ErrQuotaExceeded) {
				return true, nil
			}
			return false, err
		}
	}
	return false, nil
}

// recordWrites is the writes one read result asks of the spool, in the
// order it asks for them. It is the one place that pairs a record with
// the spool method its own kind names.
func recordWrites(sp *spool.Spool, res follow.ReadResult) []func() error {
	writes := make([]func() error, 0, len(res.Segments)+len(res.Snapshots))
	for _, seg := range res.Segments {
		writes = append(writes, func() error { return sp.WriteSegment(seg) })
	}
	for _, snap := range res.Snapshots {
		writes = append(writes, func() error { return sp.WriteMetaSnapshot(snap) })
	}
	return writes
}

// injectionValue names, for a record, what this client did with the
// project's traffic: forwarded it, or only read the files it left. It
// is the wire spelling of the shape the grant records.
func injectionValue(shape routing.Shape) string {
	if shape == routing.WithoutProxy {
		return envelope.InjectionTailOnly
	}
	return envelope.InjectionProxy
}
