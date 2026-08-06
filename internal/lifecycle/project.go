package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
)

// ErrDeclined reports that the user answered no to the data agreement.
var ErrDeclined = errors.New("data agreement declined")

// ErrNotPaired reports an operation that needs a paired device.
var ErrNotPaired = errors.New("this device is not paired")

// ErrPortOccupied reports that the proxy's port is held by something
// else, which the caller must surface loudly rather than retry.
var ErrPortOccupied = proxylife.ErrPortOccupied

// Enable starts contributing data from a project. Pairing is the
// precondition, so an unpaired device pairs first rather than failing.
func (m *Machine) Enable(projectDir string, io IO) error {
	if !m.Paired() {
		fmt.Fprintln(io.Out, "This device is not paired yet; starting pairing first.")
		if err := m.Pair(io); err != nil {
			return err
		}
	}
	return m.enableProject(projectDir, io)
}

// Disable stops contributing from a project. With purge it also asks
// the service to delete this project's uploaded but undelivered data,
// which needs a paired device to authenticate the request.
func (m *Machine) Disable(projectDir string, purge bool, io IO) error {
	hash, err := m.disableProject(projectDir, io)
	if err != nil {
		return err
	}
	if !purge {
		return nil
	}
	token, paired := m.deviceToken()
	if !paired {
		return fmt.Errorf("%w: --purge needs one to authenticate the deletion request; run `trajector login` and retry `trajector disable --purge`", ErrNotPaired)
	}
	if err := m.deps.Platform.RequestDeletion(token, hash); err != nil {
		var status *platform.StatusError
		if errors.As(err, &status) && !status.Temporary() {
			// Retrying the same request cannot succeed; do not tell the
			// user to.
			return fmt.Errorf("the project is disabled locally, but the service refused the deletion request: %w; request deletion from your account page", err)
		}
		return fmt.Errorf("the project is disabled locally, but the deletion request failed: %w; retry with `trajector disable --purge`", err)
	}
	fmt.Fprintln(io.Out, "Requested deletion of this project's uploaded, undelivered data.")
	return nil
}

// Uninstall removes every injection this binary made and stops the
// proxy. Injections come out first: once the binary is gone, a leftover
// injection would point an enabled project at a dead port.
func (m *Machine) Uninstall(deleteData bool, io IO) error {
	grants, err := m.routes.All()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var projects []string
	for _, g := range grants {
		if g.RootPath != "" && !seen[g.RootPath] {
			seen[g.RootPath] = true
			projects = append(projects, g.RootPath)
		}
	}
	sort.Strings(projects)
	removed := 0
	for _, root := range projects {
		path := claudesettings.ProjectLocalPath(root)
		if err := claudesettings.RemoveProject(path); err != nil {
			fmt.Fprintf(io.Err, "trajector: warning: could not clean %s: %v\n", path, err)
			continue
		}
		removed++
	}
	fmt.Fprintf(io.Out, "Removed injection from %d project(s).\n", removed)

	if err := claudesettings.RemoveUserHook(claudesettings.UserSettingsPath(m.deps.Home)); err != nil {
		fmt.Fprintf(io.Err, "trajector: warning: could not remove the discovery hint: %v\n", err)
	}
	m.proxy.Stop()

	if !deleteData {
		fmt.Fprintln(io.Out, "Local data kept.")
		return nil
	}
	if err := m.deps.Tokens.ClearDeviceToken(); err != nil {
		fmt.Fprintf(io.Err, "trajector: warning: could not remove the device token: %v\n", err)
	}
	for _, dir := range m.deps.Layout.Roots() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(io.Err, "trajector: warning: could not remove %s: %v\n", dir, err)
		}
	}
	fmt.Fprintln(io.Out, "Local data deleted.")
	return nil
}

// EnsureProxy brings the capture proxy up for a session and keeps the
// device's consent state honest on the way: a stale agreement pauses
// recording, and a project's own base-URL configuration is re-read in
// case the user moved it.
func (m *Machine) EnsureProxy(projectDir string, io IO) error {
	m.pauseIfAgreementStale(io)
	m.refreshUpstreamDrift(projectDir)

	if err := m.proxy.Ensure(); err != nil {
		if errors.Is(err, ErrPortOccupied) {
			return err
		}
		return fmt.Errorf("could not start the capture proxy: %w", err)
	}
	return nil
}

// pauseIfAgreementStale suspends recording device-wide when the accepted
// agreement is no longer current. Consent must always match the terms in
// force; forwarding is untouched.
func (m *Machine) pauseIfAgreementStale(io IO) {
	accepted, _, err := m.consent.AcceptedVersion()
	if err != nil || accepted == "" || accepted == consent.AgreementVersion {
		return
	}
	if err := m.routes.Pause(pauseConsent); err == nil {
		fmt.Fprintln(io.Err, "trajector: the data agreement changed; recording is paused until you reconfirm with `trajector enable`")
	}
}

// refreshUpstreamDrift re-resolves the user's own base-URL configuration
// for a project and updates the routing table when it moved, so a
// chained relay keeps working after the user reconfigures it. The hook's
// own injected value is invisible here by design; a hook has no voice,
// so what it cannot fix silently it leaves for doctor to report.
func (m *Machine) refreshUpstreamDrift(projectDir string) {
	root, err := consent.CanonicalRoot(projectDir)
	if err != nil {
		return
	}
	grant, ok, err := m.routes.Active(root)
	if err != nil || !ok {
		return
	}
	_, _, _ = m.reconcileUpstream(root, grant.Upstream)
}

// Discovery prints the one-time onboarding hint for a project that is
// not enabled. It is strictly local: no network, nothing reported, and
// only a project hash — never the path — is recorded as the
// already-hinted marker.
func (m *Machine) Discovery(projectDir string, io IO) {
	root, err := consent.CanonicalRoot(projectDir)
	if err != nil {
		return
	}
	if _, enabled, err := m.routes.Active(root); err != nil || enabled {
		return
	}
	first, err := m.consent.MarkPrompted(consent.ProjectIDHash(root))
	if err != nil || !first {
		return
	}
	fmt.Fprintln(io.Out, "trajector: this project is not contributing coding data. Run `trajector enable` to opt in. (This note appears once per project.)")
}
