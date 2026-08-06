package lifecycle

import (
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// withdrawal is what one disable did: the project identity a purge
// request is scoped by, whether a grant actually stood, and how much
// local unuploaded data left with it.
type withdrawal struct {
	hash       string
	wasEnabled bool
	spooled    int
	rejected   int
}

// disableProject withdraws a project's consent: injection removed,
// token revoked, consent recorded as denied, and the project's
// unuploaded rawcalls deleted. Steps run in the order that fails safe:
// a partial disable can only under-collect, never keep routing traffic
// while looking disabled, and rerunning completes the remainder.
func (m *Machine) disableProject(projectDir string, io IO) (withdrawal, error) {
	st, err := m.Project(projectDir)
	if err != nil {
		return withdrawal{}, err
	}
	w := withdrawal{hash: st.Hash, wasEnabled: st.Enabled}

	if !st.Injected() && !st.Enabled {
		fmt.Fprintln(io.Out, "This project is not enabled; nothing to do.")
		return w, nil
	}

	settingsPath := st.SettingsPath()
	if err := claudesettings.RemoveProject(settingsPath); err != nil {
		return w, fmt.Errorf("removing injection from %s: %w", settingsPath, err)
	}
	fmt.Fprintf(io.Out, "Removed injection from %s\n", settingsPath)

	now := m.now()
	if err := m.routes.Revoke(st.Root, now); err != nil {
		return w, fmt.Errorf("revoking the project token: %w", err)
	}
	fmt.Fprintln(io.Out, "Project token revoked; recording for this project is off.")

	if err := m.consent.SetProjectState(st.Hash, st.Root, consent.StateDenied, now); err != nil {
		return w, fmt.Errorf("recording withdrawal: %w", err)
	}

	if w.spooled, err = deleteProjectRawcalls(m.deps.Layout.SpoolDir(), st.Hash); err != nil {
		return w, fmt.Errorf("deleting local unuploaded data: %w", err)
	}
	// Rejected batches are still local unuploaded data; withdrawal must
	// reach them too.
	if w.rejected, err = upload.PurgeRejected(m.deps.Layout.RejectedDir(), st.Hash); err != nil {
		return w, fmt.Errorf("deleting local unuploaded data: %w", err)
	}
	if deleted := w.spooled + w.rejected; deleted > 0 {
		fmt.Fprintf(io.Out, "Deleted %d unuploaded rawcall(s) for this project (%d from the spool, %d from rejected batches).\n",
			deleted, w.spooled, w.rejected)
	}
	fmt.Fprintln(io.Out, "This project no longer contributes data.")
	return w, nil
}

func deleteProjectRawcalls(dir, projectIDHash string) (int, error) {
	sp, err := spool.Open(dir, 0)
	if err != nil {
		return 0, err
	}
	return sp.DeleteProject(projectIDHash)
}
