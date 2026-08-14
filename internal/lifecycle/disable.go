package lifecycle

import (
	"errors"
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
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

	// A project that looks untouched here may still be mid-withdrawal:
	// the steps below stop traffic before they delete records, so a
	// disable interrupted in between — Ctrl-C, an unremovable file, a
	// lock that timed out — leaves exactly this state with the project's
	// rawcalls still spooled. The uploader does not consult consent, so
	// the next flush would ship data already withdrawn, and until
	// 2026-08-14 this returned early past the purge, which made rerunning
	// a no-op. Only the consent-changing steps are skipped now; the purge
	// runs either way and stays silent when there is nothing to delete.
	if !st.Injected() && !st.Enabled {
		if err := m.purgeProjectRecords(st.Hash, &w); err != nil {
			return w, fmt.Errorf("deleting local unuploaded data: %w", err)
		}
		if w.spooled+w.rejected == 0 {
			fmt.Fprintln(io.Out, "This project is not enabled; nothing to do.")
			return w, nil
		}
		reportPurge(io, w)
		fmt.Fprintln(io.Out, "This project no longer contributes data.")
		return w, nil
	}

	settingsPath := st.SettingsPath()
	if err := claudesettings.RemoveProject(settingsPath); err != nil {
		return w, fmt.Errorf("removing injection from %s: %w", settingsPath, err)
	}
	fmt.Fprintf(io.Out, "Removed injection from %s\n", settingsPath)
	if err := m.restoreDisplacedBaseURL(st, settingsPath, io); err != nil {
		return w, err
	}

	now := m.now()
	if err := m.routes.Revoke(st.Root, now); err != nil {
		return w, fmt.Errorf("revoking the project token: %w", err)
	}
	fmt.Fprintln(io.Out, "Project token revoked; recording for this project is off.")

	if err := m.consent.SetProjectState(st.Hash, st.Root, consent.StateDenied, now); err != nil {
		return w, fmt.Errorf("recording withdrawal: %w", err)
	}

	if err := m.purgeProjectRecords(st.Hash, &w); err != nil {
		return w, fmt.Errorf("deleting local unuploaded data: %w", err)
	}
	reportPurge(io, w)
	fmt.Fprintln(io.Out, "This project no longer contributes data.")
	return w, nil
}

// restoreDisplacedBaseURL puts back a base URL of the user's own that
// enable overwrote. Injection writes ANTHROPIC_BASE_URL into the
// project-local settings file, which is also the first link of the
// configuration chain, so a relay kept there was replaced on the way in
// — and removal, which deletes exactly what trajector wrote, took the
// last copy of it. The user's next session then went to the official
// endpoint carrying relay credentials, silently. 2026-08-14.
//
// The grant holds what the chain said while the user was watching, so it
// is the copy to restore, and only when the chain now names nothing at
// all: a value still visible elsewhere was never displaced. A disable
// run inside a Claude Code session reads as masked rather than as
// nothing and is left alone, for the same reason the session hook leaves
// a masked upstream alone.
func (m *Machine) restoreDisplacedBaseURL(st ProjectStatus, settingsPath string, io IO) error {
	if !st.Enabled || st.Upstream == "" || st.Upstream == capture.Anthropic.OfficialUpstream {
		return nil
	}
	if _, _, res := claudesettings.ExternalBaseURL(st.Root, m.deps.Home, m.deps.Getenv); res != claudesettings.BaseURLNone {
		return nil
	}
	if err := claudesettings.SetBaseURL(settingsPath, st.Upstream); err != nil {
		return fmt.Errorf("restoring your own base URL in %s: %w", settingsPath, err)
	}
	fmt.Fprintf(io.Out, "Restored your own base URL in %s: %s\n", settingsPath, st.Upstream)
	return nil
}

// purgeProjectRecords deletes one project's unuploaded records from both
// places they can sit, recording how many left from each. Rejected
// batches are still local unuploaded data, so withdrawal must reach them
// too — and both are attempted even when the first fails, so one
// unremovable file cannot leave the other store untouched.
func (m *Machine) purgeProjectRecords(projectIDHash string, w *withdrawal) error {
	sp, err := m.spool()
	if err != nil {
		return err
	}
	var errs []error
	if w.spooled, err = sp.DeleteProject(projectIDHash); err != nil {
		errs = append(errs, err)
	}
	if w.rejected, err = upload.PurgeRejected(m.deps.Layout.RejectedDir(), projectIDHash); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func reportPurge(io IO, w withdrawal) {
	if deleted := w.spooled + w.rejected; deleted > 0 {
		fmt.Fprintf(io.Out, "Deleted %d unuploaded rawcall(s) for this project (%d from the spool, %d from rejected batches).\n",
			deleted, w.spooled, w.rejected)
	}
}
