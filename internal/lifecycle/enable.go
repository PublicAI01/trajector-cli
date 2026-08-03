package lifecycle

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

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
	root, err := consent.CanonicalRoot(projectDir)
	if err != nil {
		return err
	}
	hash := consent.ProjectIDHash(root)
	if err := m.confirmAgreement(io); err != nil {
		return err
	}

	if key, found := claudesettings.UnsupportedChannel(root, m.deps.Home, m.deps.Getenv); found {
		return fmt.Errorf("%s is set: Bedrock and Vertex channels are not supported and nothing was injected", key)
	}

	upstream := capture.Anthropic.OfficialUpstream
	external, source, thirdParty := claudesettings.ExternalBaseURL(root, m.deps.Home, m.deps.Getenv)
	if thirdParty {
		upstream = external
		fmt.Fprintf(io.Out, "Detected an existing base URL (%s): %s\n", source, external)
		fmt.Fprintln(io.Out, "Your traffic will keep flowing through it unchanged. Records from this")
		fmt.Fprintln(io.Out, "project are marked as third-party origin; reward terms are the same")
		fmt.Fprintln(io.Out, "regardless of origin.")
	}

	settingsPath := claudesettings.ProjectLocalPath(root)
	snap, err := takeSnapshots(settingsPath, m.deps.Layout.RoutingTable(), m.deps.Layout.ConsentFile(), filepath.Join(root, ".gitignore"))
	if err != nil {
		return err
	}

	if err := m.installAndVerify(io, root, hash, upstream, settingsPath); err != nil {
		if restoreErr := snap.restore(); restoreErr != nil {
			return fmt.Errorf("%w (rollback incomplete: %v)", err, restoreErr)
		}
		return fmt.Errorf("%w (all changes rolled back)", err)
	}
	return nil
}

func (m *Machine) installAndVerify(io IO, root, hash, upstream, settingsPath string) error {
	token, err := m.projectToken(root)
	if err != nil {
		return err
	}
	now := m.now()
	if err := m.routes.Grant(routing.Grant{
		Token:         token,
		ProjectIDHash: hash,
		RootPath:      root,
		Upstream:      upstream,
		GrantedAt:     now,
	}); err != nil {
		return fmt.Errorf("updating routing table: %w", err)
	}
	if err := m.consent.SetProjectState(hash, root, consent.StateGranted, now); err != nil {
		return fmt.Errorf("recording project consent: %w", err)
	}
	if err := claudesettings.InjectProject(settingsPath, m.proxy.BaseURL(token), hookCommand(m.deps.ExecPath, "ensure-proxy")); err != nil {
		return fmt.Errorf("injecting %s: %w", settingsPath, err)
	}
	fmt.Fprintf(io.Out, "Injected %s (base URL and session hooks)\n", settingsPath)

	action, err := claudesettings.EnsureGitIgnored(root, ".claude/settings.local.json")
	if err != nil {
		return fmt.Errorf("ensuring .gitignore covers the injected settings: %w", err)
	}
	if action == claudesettings.IgnoreAppended {
		fmt.Fprintln(io.Out, "Added .claude/settings.local.json to .gitignore")
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
	fmt.Fprint(io.Out, "Do you accept the data agreement? [yes/no]: ")

	line, err := bufio.NewReader(io.In).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("reading agreement answer: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "yes", "y":
	default:
		return ErrDeclined
	}
	if err := m.consent.AcceptAgreement(consent.AgreementVersion, m.now()); err != nil {
		return err
	}
	// Capture paused for reconfirmation may resume now that the
	// current terms are accepted.
	return m.routes.Resume(pauseConsent)
}

// projectToken reuses the active token when the project is already
// enabled — re-running enable must repair, not re-key — and mints a
// fresh 128-bit token otherwise.
func (m *Machine) projectToken(root string) (string, error) {
	if route, ok, err := m.routes.Active(root); err != nil {
		return "", err
	} else if ok {
		return route.Token, nil
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
		if errors.Is(err, proxylife.ErrPortOccupied) {
			return fmt.Errorf("self-check failed: %v; refusing to route credentials at it (run `trajector doctor`)", err)
		}
		return fmt.Errorf("self-check failed: %w", err)
	}

	reply, err := m.proxy.Selfcheck(token)
	if err != nil {
		return fmt.Errorf("self-check request failed: %w", err)
	}
	switch {
	case reply.Service != apiproxy.ServiceName:
		return fmt.Errorf("self-check failed: %s did not answer as a trajector proxy", m.deps.ProxyAddr)
	case !reply.TokenKnown:
		return fmt.Errorf("self-check failed: the proxy does not know this project's token")
	case !reply.Recording:
		return fmt.Errorf("self-check failed: %s", notRecordingReason(reply))
	case !reply.SpoolWritable:
		return fmt.Errorf("self-check failed: the capture spool at %s is not writable (check disk space and quota)", m.deps.Layout.SpoolDir())
	}
	return nil
}

// notRecordingReason turns the proxy's verdict into something the user
// can act on. Reporting only that this project would not be recorded
// leaves a signed-out user with no idea what to do about it.
func notRecordingReason(reply proxylife.Selfcheck) string {
	switch reply.PauseReason {
	case pauseSignedOut:
		return "this device is signed out, so nothing is being recorded; run `trajector login` and try again"
	case pauseConsent:
		return "the data agreement changed and recording is paused until you reconfirm it"
	}
	if reply.Decision == string(routing.ForwardOnlyRevoked) {
		return "this project's token is revoked; run `trajector enable` again to re-grant it"
	}
	return "the proxy would not record this project"
}
