package lifecycle

import (
	"fmt"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// Disable withdraws a project's consent: injection removed, token
// revoked, consent recorded as denied, and the project's unuploaded
// rawcalls deleted. It reports the project hash so the caller can
// forward a deletion request to the service. Steps run in the order
// that fails safe: a partial disable can only under-collect, never
// keep routing traffic while looking disabled, and rerunning completes
// the remainder.
func (m *Machine) disableProject(projectDir string, io IO) (projectIDHash string, err error) {
	root, err := consent.CanonicalRoot(projectDir)
	if err != nil {
		return "", err
	}
	hash := consent.ProjectIDHash(root)
	settingsPath := claudesettings.ProjectLocalPath(root)

	_, injected := claudesettings.InjectedBaseURL(settingsPath)
	_, active, err := m.routes.Active(root)
	if err != nil {
		return "", err
	}
	if !injected && !active {
		fmt.Fprintln(io.Out, "This project is not enabled; nothing to do.")
		return hash, nil
	}

	if err := claudesettings.RemoveProject(settingsPath); err != nil {
		return "", fmt.Errorf("removing injection from %s: %w", settingsPath, err)
	}
	fmt.Fprintf(io.Out, "Removed injection from %s\n", settingsPath)

	now := m.now()
	if err := m.routes.Revoke(root, now); err != nil {
		return "", fmt.Errorf("revoking the project token: %w", err)
	}
	fmt.Fprintln(io.Out, "Project token revoked; recording for this project is off.")

	if err := m.consent.SetProjectState(hash, root, consent.StateDenied, now); err != nil {
		return "", fmt.Errorf("recording withdrawal: %w", err)
	}

	spooled, err := deleteProjectRawcalls(m.deps.Layout.SpoolDir(), hash)
	if err != nil {
		return "", fmt.Errorf("deleting local unuploaded data: %w", err)
	}
	// Rejected batches are still local unuploaded data; withdrawal must
	// reach them too.
	rejected, err := upload.PurgeRejected(m.deps.Layout.RejectedDir(), hash)
	if err != nil {
		return "", fmt.Errorf("deleting local unuploaded data: %w", err)
	}
	if deleted := spooled + rejected; deleted > 0 {
		fmt.Fprintf(io.Out, "Deleted %d unuploaded rawcall(s) for this project (%d from the spool, %d from rejected batches).\n",
			deleted, spooled, rejected)
	}
	fmt.Fprintln(io.Out, "This project no longer contributes data.")
	return hash, nil
}

func deleteProjectRawcalls(dir, projectIDHash string) (int, error) {
	sp, err := spool.Open(dir, 0)
	if err != nil {
		return 0, err
	}
	return sp.DeleteProject(projectIDHash)
}
