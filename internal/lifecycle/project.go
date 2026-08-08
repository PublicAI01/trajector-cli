package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// ErrDeclined reports that the user answered no to the data agreement.
var ErrDeclined = errors.New("data agreement declined")

// ErrNotPaired reports an operation that needs a paired device.
var ErrNotPaired = errors.New("this device is not paired")

// ErrPortOccupied reports that the proxy's port is held by something
// else, which the caller must surface loudly rather than retry.
var ErrPortOccupied = proxylife.ErrPortOccupied

// ErrProxyUnverified reports a port holder that failed admin-token
// authentication: possibly this user's own proxy whose published token
// went missing or stale, so its remedy must never be the foreign-process
// one.
var ErrProxyUnverified = proxylife.ErrProxyUnverified

// The one instruction every surface prints with each port-holder
// verdict, so the advice cannot drift between surfaces. Only ProxyRemedy
// maps a verdict to its instruction.
const (
	portOccupiedRemedy    = "Enabled projects route API credentials at this address; find and stop the process holding the port, or run `trajector disable` in enabled projects."
	proxyUnverifiedRemedy = "This is usually an authentication problem (the proxy's published admin token is missing or stale), not a foreign process. The proxy publishes a fresh token each time it starts and exits on its own once idle, so a later session usually clears it; there is no process to stop."
)

// ProxyRemedy is the follow-up instruction a surface prints under a
// failed port-holder verdict, empty when the verdict's own words are
// the whole story. Advising the user to stop the port's holder is
// reserved for a proven stranger: an unverified holder may be their own
// proxy.
func ProxyRemedy(why error) string {
	switch {
	case errors.Is(why, ErrPortOccupied):
		return portOccupiedRemedy
	case errors.Is(why, ErrProxyUnverified):
		return proxyUnverifiedRemedy
	}
	return ""
}

// Enable starts contributing data from a project. Pairing is the
// precondition, so an unpaired device pairs first rather than failing.
func (m *Machine) Enable(projectDir string, io IO) error {
	m.warnNonDefaultEndpoint(io.Out)
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
// which needs a paired device to authenticate the request. The purge
// request is sent even when no grant stands right now (wasEnabled
// false): data may have been uploaded under an earlier enable, and the
// service scopes deletion by project hash, not by the current grant.
func (m *Machine) Disable(projectDir string, purge bool, io IO) error {
	w, err := m.disableProject(projectDir, io)
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
	if err := m.deps.Platform.RequestDeletion(token, w.hash); err != nil {
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

// leftoverIgnoreRules reports which of the enable-written .gitignore
// lines are still present under root. Uninstall only ever reports
// them: the file is the user's, so removing lines from it is their
// call, never this binary's.
func leftoverIgnoreRules(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	present := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var rules []string
	for _, rule := range projectIgnoreRules {
		if present[rule] {
			rules = append(rules, rule)
		}
	}
	return rules
}

// Uninstall removes every injection this binary made and stops the
// proxy. Injections come out first: once the binary is gone, a leftover
// injection would point an enabled project at a dead port. Whether
// local data goes too is the user's call: a caller that has not already
// decided (deleteData false) is asked here, before anything changes.
func (m *Machine) Uninstall(deleteData bool, io IO) error {
	if !deleteData {
		deleteData, _ = askYesNo(io, "Delete local data (captured rawcalls, configuration, device token)? [y/N]: ")
	}
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
	for _, root := range projects {
		if leftover := leftoverIgnoreRules(root); len(leftover) > 0 {
			fmt.Fprintf(io.Out, "Left %s in %s; remove those lines yourself if you no longer want them.\n",
				strings.Join(leftover, ", "), filepath.Join(root, ".gitignore"))
		}
	}

	if err := claudesettings.RemoveUserHook(claudesettings.UserSettingsPath(m.deps.Home)); err != nil {
		fmt.Fprintf(io.Err, "trajector: warning: could not remove the discovery hint: %v\n", err)
	}
	if err := m.proxy.Stop(); err != nil {
		fmt.Fprintf(io.Err, "trajector: warning: the proxy was not stopped: %v\n", err)
	}

	if deleteData {
		if err := m.deps.Tokens.ClearDeviceToken(); err != nil {
			fmt.Fprintf(io.Err, "trajector: warning: could not remove the device token: %v\n", err)
		}
		for _, dir := range m.deps.Layout.Roots() {
			if err := os.RemoveAll(dir); err != nil {
				fmt.Fprintf(io.Err, "trajector: warning: could not remove %s: %v\n", dir, err)
			}
		}
		fmt.Fprintln(io.Out, "Local data deleted.")
	} else {
		fmt.Fprintln(io.Out, "Local data kept.")
	}
	fmt.Fprintf(io.Out, "Done. To finish, delete the binary itself: %s\n", m.deps.ExecPath)
	return nil
}

// EnsureProxy brings the capture proxy up for a session and keeps the
// device's consent state honest on the way: a stale agreement pauses
// recording, and a project's own base-URL configuration is re-read in
// case the user moved it.
func (m *Machine) EnsureProxy(projectDir string, io IO) error {
	m.pauseIfAgreementStale(io)
	m.refreshUpstreamDrift(projectDir, io)

	if err := m.proxy.Ensure(); err != nil {
		if errors.Is(err, ErrPortOccupied) || errors.Is(err, ErrProxyUnverified) {
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
	if err := m.routes.Pause(routing.PauseConsentReconfirm); err == nil {
		fmt.Fprintln(io.Err, "trajector: the data agreement changed; recording is paused until you reconfirm with `trajector enable`")
	}
}

// refreshUpstreamDrift re-resolves the user's own base-URL configuration
// for a project and updates the routing table when it moved, so a
// chained relay keeps working after the user reconfigures it. A hook
// has next to no voice, so a change is never only spoken: the move is
// recorded on the grant where status shows it afterwards, and the
// stderr line here is a best-effort courtesy.
func (m *Machine) refreshUpstreamDrift(projectDir string, io IO) {
	st, err := m.Project(projectDir)
	if err != nil || !st.Enabled {
		return
	}
	want, moved, refused, _ := m.reconcileUpstream(st.Root, st.Upstream)
	switch {
	case moved:
		fmt.Fprintf(io.Err, "trajector: this project's upstream moved to %s (its base-URL configuration changed; `trajector status` has the details)\n", want.upstream)
	case refused:
		fmt.Fprintf(io.Err, "trajector: refusing to move this project's upstream to %s: %s\n", want.upstream, nonLoopbackUpstreamRemedy)
	}
}

// Discovery prints the one-time onboarding hint for a project that is
// not enabled. It is strictly local: no network, nothing reported, and
// only a project hash — never the path — is recorded as the
// already-hinted marker.
func (m *Machine) Discovery(projectDir string, io IO) {
	st, err := m.Project(projectDir)
	if err != nil || st.Enabled {
		return
	}
	first, err := m.consent.MarkPrompted(st.Hash)
	if err != nil || !first {
		return
	}
	fmt.Fprintln(io.Out, "trajector: this project is not contributing coding data. Run `trajector enable` to opt in. (This note appears once per project.)")
}
