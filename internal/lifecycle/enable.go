package lifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// projectIgnoreRules are the .gitignore lines an enabled project
// carries: the injected settings file with its transient siblings,
// which embed a consent token, and both forms of a diagnostic bundle.
// The bundle rules land at enable time so the archive and its unpacked
// directory are ignored before either exists; uninstall names exactly
// this list when it points at leftover lines, so the two surfaces
// cannot drift.
var projectIgnoreRules = append([]string{claudesettings.ProjectLocalIgnoreRule}, doctorBundleIgnoreRules...)

// hookCommand renders the shell command injected into settings hooks.
func hookCommand(execPath, subcommand string) string {
	if strings.ContainsAny(execPath, " \t") {
		execPath = `"` + execPath + `"`
	}
	return execPath + " hook " + subcommand
}

// enableProject drives the enable state machine to completion or rolls
// back. It is idempotent and transactional: it either reaches the fully
// injected, self-checked state or restores every file it touched. The
// invariant it protects: a project with an injected base URL always has
// its token in the routing table and both session hooks present — a
// half-enabled project routing traffic at a dead port must be
// impossible.
func (m *Machine) enableProject(projectDir string, io IO) error {
	st, err := m.Project(projectDir)
	if err != nil {
		return err
	}
	if err := m.confirmAgreement(io); err != nil {
		return err
	}

	want := m.desiredUpstream(st.Root)
	if want.unsupportedKey != "" {
		return fmt.Errorf("%s is set: Bedrock and Vertex channels are not supported and nothing was injected", want.unsupportedKey)
	}
	upstream := want.upstream
	// A standing grant of this project's own is the one record of where
	// its traffic went that survives our injection standing in the
	// configuration chain. The unattended reconcile asks the same
	// question through the same predicate, so the two cannot drift.
	keep := want.keepsRecordedUpstream(st)
	if keep && st.Upstream != upstream {
		// The chain names no base URL of the user's own — but our own
		// injection stands in the first file it reads, and this project
		// already has a grant naming one, so the silence is ours: enable
		// overwrote that value on the way in. Re-granting the official
		// endpoint here would send a relay user's credentialed traffic
		// elsewhere on a guess, which is what the session hook refuses to
		// do for a masked upstream. Repair must not re-key the upstream
		// any more than it re-keys the token. 2026-08-14.
		upstream = st.Upstream
		fmt.Fprintf(io.Out, "Keeping the base URL this project was enabled with: %s\n", upstream)
	} else if want.masked && !keep {
		// Masked and nothing of this project's own to fall back on: our
		// injection stands in the shell where the user's own base URL
		// would be, and no grant records what it was. want.upstream is
		// the official endpoint here, but only as the best guess a
		// surface that must name one gets — and granting is not such a
		// surface. Until 2026-08-16 this fell through and granted that
		// guess silently, so a relay user who ran enable from inside a
		// Claude Code session (a fresh project, or the same one right
		// after disable) had every later request carry their relay's
		// credentials to the official endpoint instead. reconcileUpstream
		// and disable already refuse to act on masked; granting is the
		// one path that did not. 2026-08-16.
		return ErrUpstreamMasked
	} else if want.external {
		fmt.Fprintf(io.Out, "Detected an existing base URL (%s): %s\n", want.source, want.upstream)
		fmt.Fprintln(io.Out, "Your traffic will keep flowing through it unchanged. Records from this")
		fmt.Fprintln(io.Out, "project are marked as third-party origin; reward terms are the same")
		fmt.Fprintln(io.Out, "regardless of origin.")
	}

	// Nothing may enter the routing table that the data path cannot
	// follow. The unattended reconcile has asked this of every upstream
	// it writes since it existed; enable never did, so a base URL that
	// Claude Code accepts and Go's url.Parse refuses (a password holding
	// '|', a bare '%', a missing scheme) was granted here and then met
	// the proxy's unusable-route fallback. Checked after the branches
	// above so a grant carried forward by keep is judged too: a table
	// written by an older build holds exactly these values, and running
	// enable is how a user finds out. See ErrUpstreamUnroutable.
	// 2026-08-24.
	if !platform.RoutableURL(upstream) {
		if want.source != "" {
			return fmt.Errorf("%w (the value comes from your %s)", ErrUpstreamUnroutable, want.source)
		}
		return ErrUpstreamUnroutable
	}

	// The routing table and the consent file are shared with concurrent
	// processes, so rollback restores them entry-wise through their
	// stores' serialized updates; a byte-for-byte restore would hand a
	// concurrent enable's grant to the rollback. Only the project-local
	// files are snapshotted whole.
	snap, err := takeSnapshots(st.SettingsPath(), filepath.Join(st.Root, ".gitignore"))
	if err != nil {
		return err
	}
	grants, err := m.routes.SnapshotGrants(st.Root)
	if err != nil {
		return err
	}
	decision, err := m.consent.SnapshotProject(st.Hash)
	if err != nil {
		return err
	}

	if err := m.installAndVerify(io, st, upstream); err != nil {
		restoreErr := errors.Join(snap.restore(), m.routes.RestoreGrants(grants), m.consent.RestoreProject(decision))
		if restoreErr != nil {
			return fmt.Errorf("%w (rollback incomplete: %v)", err, restoreErr)
		}
		return fmt.Errorf("%w (all changes rolled back)", err)
	}
	return nil
}

func (m *Machine) installAndVerify(io IO, st ProjectStatus, upstream string) error {
	token, err := projectToken(st)
	if err != nil {
		return err
	}
	settingsPath := st.SettingsPath()
	now := m.now()
	if err := m.routes.Grant(routing.Grant{
		Token:         token,
		ProjectIDHash: st.Hash,
		RootPath:      st.Root,
		Upstream:      upstream,
		GrantedAt:     now,
	}); err != nil {
		return fmt.Errorf("updating routing table: %w", err)
	}
	if err := m.consent.SetProjectState(st.Hash, st.Root, consent.StateGranted, now); err != nil {
		return fmt.Errorf("recording project consent: %w", err)
	}
	if err := claudesettings.InjectProject(settingsPath, m.proxy.BaseURL(token), hookCommand(m.deps.ExecPath, "ensure-proxy")); err != nil {
		return fmt.Errorf("injecting %s: %w", settingsPath, err)
	}
	fmt.Fprintf(io.Out, "Injected %s (base URL and session hooks)\n", settingsPath)

	var appended []string
	symlinked := false
	for _, rule := range projectIgnoreRules {
		action, err := claudesettings.EnsureGitIgnored(st.Root, rule)
		if err != nil {
			return fmt.Errorf("ensuring .gitignore covers %s: %w", rule, err)
		}
		switch action {
		case claudesettings.IgnoreAppended:
			appended = append(appended, rule)
		case claudesettings.IgnoreSymlinked:
			symlinked = true
		}
	}
	if len(appended) > 0 {
		fmt.Fprintf(io.Out, "Added %s to .gitignore\n", strings.Join(appended, ", "))
	}
	if symlinked {
		fmt.Fprintf(io.Err, "WARNING: .gitignore is a symbolic link and was left alone; add %s to your git ignores so the injected settings and diagnostic bundles are never committed.\n", strings.Join(projectIgnoreRules, ", "))
	}

	if err := m.selfCheck(token); err != nil {
		return err
	}
	fmt.Fprintln(io.Out, "Self-check passed: routing and recording verified end to end.")
	fmt.Fprintln(io.Out, "This project now contributes data. Run `trajector disable` here to stop.")
	return nil
}

// confirmAgreement shows the agreement and records the explicit
// answer. An acceptance recorded for an older agreement version is
// stale: the terms changed, so the user must confirm again.
func (m *Machine) confirmAgreement(io IO) error {
	version, _, err := m.consent.AcceptedVersion()
	if err != nil {
		return err
	}
	if version == consent.AgreementVersion {
		return nil
	}
	if version != "" {
		fmt.Fprintln(io.Out, "The data agreement changed since you last accepted it.")
	}
	fmt.Fprintln(io.Out, consent.AgreementText)
	fmt.Fprintln(io.Out)
	yes, err := askYesNo(io, "Do you accept the data agreement? [yes/no]: ")
	if err != nil {
		return fmt.Errorf("reading agreement answer: %w", err)
	}
	if !yes {
		return ErrDeclined
	}
	if err := m.consent.AcceptAgreement(consent.AgreementVersion, m.now()); err != nil {
		return err
	}
	// Capture paused for reconfirmation may resume now that the
	// current terms are accepted.
	return m.routes.Resume(routing.PauseConsentReconfirm)
}

// projectToken reuses the active token when the project is already
// enabled — re-running enable must repair, not re-key — and mints a
// fresh 128-bit token otherwise.
func projectToken(st ProjectStatus) (string, error) {
	if st.Enabled {
		return st.Token, nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// selfCheck proves the injected route works before enable reports
// success: the proxy is up and this exact token routes and records.
// No upstream call is made and nothing is billed.
func (m *Machine) selfCheck(token string) error {
	if err := m.proxy.Ensure(); err != nil {
		if remedy := ProxyRemedy(err); remedy != "" {
			return fmt.Errorf("self-check failed: %v. %s", err, remedy)
		}
		return fmt.Errorf("self-check failed: %w", err)
	}
	if _, err := m.verifyRoute(token); err != nil {
		return fmt.Errorf("self-check failed: %v", err)
	}
	return nil
}

// verifyRoute asks the live proxy what it would do with token — routed,
// recorded, spool writable — over the exact injected base-URL shape and
// without an upstream call. enable proves a fresh route with it and
// doctor re-proves an existing one; the returned error is the one
// explanation both present.
func (m *Machine) verifyRoute(token string) (proxylife.Selfcheck, error) {
	reply, err := m.proxy.Selfcheck(token)
	if err != nil {
		return reply, fmt.Errorf("the self-check request failed: %w", err)
	}
	return reply, m.explainSelfcheck(reply)
}

// explainSelfcheck turns a selfcheck reply into the error a user can
// act on, nil when the route records.
func (m *Machine) explainSelfcheck(reply proxylife.Selfcheck) error {
	switch {
	case !reply.IsOurs():
		return fmt.Errorf("%s did not answer as a trajector proxy", m.deps.ProxyAddr)
	case !reply.TokenKnown:
		return errors.New("the proxy does not know this project's token")
	case !reply.Recording:
		return errors.New(notRecordingReason(reply))
	case !reply.SpoolWritable:
		return fmt.Errorf("the capture spool at %s is not writable (check disk space and quota)", m.deps.Layout.SpoolDir())
	}
	return nil
}

// notRecordingReason turns the proxy's verdict into something the user
// can act on. Reporting only that this project would not be recorded
// leaves a signed-out user with no idea what to do about it.
func notRecordingReason(reply proxylife.Selfcheck) string {
	if reply.PauseReason != "" {
		return "nothing is being recorded: " + routing.PauseReason(reply.PauseReason).Explain()
	}
	if reply.Decision == string(routing.ForwardOnlyRevoked) {
		return "this project's token is revoked; run `trajector enable` again to re-grant it"
	}
	return "the proxy would not record this project"
}
