package lifecycle

import (
	"errors"
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// SessionIDEnv is the variable Claude Code sets in every process a
// coding session starts, naming that session. It is read for one
// purpose only: to say which session a command acts on when the user
// names none. Nothing about where data goes is ever read from it.
const SessionIDEnv = "CLAUDE_CODE_SESSION_ID"

// CurrentSessionID is the session this process was started from, or
// empty when it was not started from one.
func (m *Machine) CurrentSessionID() string {
	return m.deps.Getenv(SessionIDEnv)
}

// forgetIntro is the whole of what the user is told before anything is
// deleted. Its three statements are the promise this command keeps:
// only what this machine still holds and has not uploaded goes; what
// was uploaded is deleted elsewhere; and the session keeps being
// recorded, because deleting is not withdrawing consent.
const forgetIntro = "Forgetting session %s: deleting its records that have not been uploaded yet from this machine. " +
	"Uploaded data is deleted from the Dashboard instead. " +
	"Recording of this session continues; only what was collected so far is gone.\n"

// Forget deletes what this machine still holds for one coding session
// and has not uploaded, wherever it waits: the session's recorded API
// calls and the records read from its session file — including those
// of its sub-agents, which carry the same session id — in every slot
// the spool keeps, the records held back from upload among them, and
// the same records in every quarantined batch, which are unuploaded
// local data as well. Acknowledged uploads are no longer in
// the spool, so they are out of reach here by construction; the session
// file itself is never touched; and the reader's position is already
// past what is deleted, so recording carries on without re-reading it.
//
// Records pinned by an upload in progress are deleted like any other,
// exactly as a project's withdrawal deletes them: the uploader sends
// whatever of a pinned batch is still on disk and releases the batch
// when nothing is. The batch in flight is never cancelled here: it is
// left to the acknowledgement it waits for. A batch the service stored
// whose acknowledgement has not arrived yet is uploaded data, and is
// deleted where uploaded data is.
func (m *Machine) Forget(sessionID string, io IO) error {
	if sessionID == "" {
		return errors.New("no session id to forget")
	}
	sp, err := m.spool()
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, forgetIntro, sessionID)
	rawcalls, records, err := sp.DeleteSession(sessionID)
	if err != nil {
		return fmt.Errorf("deleting session %s: %w", sessionID, err)
	}
	quarantined, err := upload.PurgeRejectedSession(m.deps.Layout.RejectedDir(), sessionID)
	if err != nil {
		return fmt.Errorf("deleting session %s from the quarantined batches: %w", sessionID, err)
	}
	// One count, because the user asked one question: how much of this
	// session is gone. Which store held a record is how this machine
	// keeps records, not something the user chose.
	fmt.Fprintf(io.Out, "Deleted %d record(s) of session %s.\n", rawcalls+records+quarantined, sessionID)
	return nil
}
