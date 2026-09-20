package lifecycle

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// What enable says, in the words the user reads. Each sentence states a
// fact about this device or this project and, where the fact costs the
// user something, the ways out of it; none of them asks for a second
// agreement.
const (
	// deviceWideTerms follows the agreement text: accepting it covers
	// every project on this device, not only the one being enabled.
	deviceWideTerms = "You are accepting these terms for this device, not only for this project. Any project you enable on this device is covered."

	// noProxyRecordsNothing is the consequence in the shape without a
	// base URL, where the hooks are the only source. Enabling is not
	// refused — the setting is the user's, or their organization's, to
	// change — but it is not done silently either.
	noProxyRecordsNothing = "Judged from configuration readable on this machine, Claude Code will not run trajector's hooks here, so --no-proxy would record nothing from this project. Either accept that nothing is recorded for now, or run trajector enable without --no-proxy (the proxy records; /remote-control inside this project becomes unavailable, claude remote-control still works)."

	contributesNow = "This project now contributes data. Run `trajector disable` here to stop."
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

// projectHooks renders one command per hook a project injection
// installs, so enable and doctor spell them once and neither names the
// hooks the injection is made of.
func projectHooks(execPath string) claudesettings.HookCommands {
	subcommands := claudesettings.ProjectHookSubcommands()
	hooks := make(claudesettings.HookCommands, len(subcommands))
	for _, subcommand := range subcommands {
		hooks[subcommand] = hookCommand(execPath, subcommand)
	}
	return hooks
}

// enableProject drives the enable state machine to completion or rolls
// back. It is idempotent and transactional: every change it makes goes
// on a ledger with the way to take it back, and a failure replays the
// ledger in reverse. Five artifacts are on it — the routing grant, the
// project's consent record, its session file registry, the
// project-local settings file, and the .gitignore lines this install
// appended — so enable either reaches the fully injected, self-checked
// state or leaves all five as it found them.
//
// Two writes stand outside the ledger, both made before the first
// change to any of the five: accepting the data agreement, and
// lifting the device-wide pause a changed agreement set. They are the
// answer the user gave about this device, not an edit enable made to
// this project, and a failure in this project does not withdraw it.
//
// The invariant it protects: a project with an injected base URL always
// has its token in the routing table and every session hook
// present — a half-enabled project routing traffic at a dead port must
// be impossible.
//
// Everything the user is told before the install — the agreement, a
// hook configuration that will not load, a base URL of their own, the
// session files already on disk — is said before any file changes, so
// the one answer enable ever waits for is given with the facts in
// view. Re-running enable on an enabled project walks the same steps:
// that is how an injection made by an older build is completed.
func (m *Machine) enableProject(projectDir string, shape routing.Shape, io IO) error {
	// Every prompt in one enable must read through one buffered reader: a
	// second bufio over the same stream would find the bytes the first
	// one buffered ahead already gone. bufio.NewReader hands this same
	// reader back when the prompts wrap it again.
	io.In = bufio.NewReader(io.In)
	st, err := m.Project(projectDir)
	if err != nil {
		return err
	}
	if err := m.confirmAgreement(io); err != nil {
		return err
	}
	if proceed, err := m.confirmHooksWillRun(io, st.Root, shape); err != nil || !proceed {
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

	// The session files this project already has are counted and named
	// here, before anything is written: the count is part of what the
	// user is enabling, and reading them is not asked about separately.
	earlier, err := discover.Walk(st.Root, m.claude().ConfigDir)
	if err != nil {
		return fmt.Errorf("looking for this project's session files: %w", err)
	}
	for _, line := range report.EarlierSessionLines(earlier) {
		fmt.Fprintln(io.Out, line)
	}

	// The routing table, the consent file, and the project's .gitignore
	// are all shared with concurrent processes, so rollback undoes them
	// entry-wise through their own writers; a byte-for-byte restore would
	// hand a concurrent enable's grant — or a concurrent bundle's ignore
	// line — to the rollback. .gitignore was snapshotted whole until
	// 2026-08-27; see RemoveGitIgnored for what that cost. Only the
	// project-local settings file, which is this tool's own, is
	// snapshotted whole. All three are read before the first change, so
	// an enable that must refuse — a symbolic link where the settings
	// file belongs — refuses with nothing to take back.
	prior, err := m.readBeforeChanging(st)
	if err != nil {
		return err
	}

	var ledger enableLedger
	if err := m.installAndVerify(io, st, upstream, shape, earlier, prior, &ledger); err != nil {
		if undoErr := ledger.undo(); undoErr != nil {
			return fmt.Errorf("%w (rollback incomplete: %v)", err, undoErr)
		}
		return fmt.Errorf("%w (all changes rolled back)", err)
	}
	return nil
}

// priorState is what enable read of the artifacts it is about to
// change. Each value carries the whole of what one undo needs, so an
// undo recorded on the ledger stands on its own.
type priorState struct {
	settings fileSnapshot
	grants   routing.GrantSnapshot
	decision consent.ProjectSnapshot
}

func (m *Machine) readBeforeChanging(st report.ProjectStatus) (priorState, error) {
	var prior priorState
	var err error
	if prior.settings, err = takeSnapshot(st.SettingsPath()); err != nil {
		return prior, err
	}
	if prior.grants, err = m.routes.SnapshotGrants(st.Root); err != nil {
		return prior, err
	}
	prior.decision, err = m.consent.SnapshotProject(st.Hash)
	return prior, err
}

func (m *Machine) installAndVerify(io IO, st report.ProjectStatus, upstream string, shape routing.Shape, earlier discover.Result, prior priorState, ledger *enableLedger) error {
	token, err := projectToken(st)
	if err != nil {
		return err
	}
	settingsPath := st.SettingsPath()
	now := m.now()
	ledger.record(func() error { return m.routes.RestoreGrants(prior.grants) })
	if err := m.routes.Grant(routing.Grant{
		Token:         token,
		ProjectIDHash: st.Hash,
		RootPath:      st.Root,
		Upstream:      upstream,
		GrantedAt:     now,
		Shape:         shape,
	}); err != nil {
		return fmt.Errorf("updating routing table: %w", err)
	}
	ledger.record(func() error { return m.consent.RestoreProject(prior.decision) })
	if err := m.consent.SetProjectState(st.Hash, st.Root, consent.StateGranted, now); err != nil {
		return fmt.Errorf("recording project consent: %w", err)
	}
	if err := m.registerEarlierSessions(st.Hash, earlier, ledger); err != nil {
		return fmt.Errorf("registering this project's session files: %w", err)
	}
	m.offerOptionalSettings(io, st)
	ledger.record(prior.settings.restore)
	restored, unrestored, err := m.injectProject(st, token, shape)
	if err != nil {
		return fmt.Errorf("injecting %s: %w", settingsPath, err)
	}
	if restored != "" {
		fmt.Fprintf(io.Out, "Restored your own base URL in %s: %s\n", settingsPath, restored)
	}
	if unrestored != "" {
		fmt.Fprintf(io.Err, "trajector: WARNING: %s\n", unrestoredBaseURLWarning(settingsPath, unrestored))
	}
	if shape == routing.WithoutProxy {
		fmt.Fprintf(io.Out, "Injected %s (session hooks, no base URL)\n", settingsPath)
	} else {
		fmt.Fprintf(io.Out, "Injected %s (base URL and session hooks)\n", settingsPath)
	}

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
			ledger.record(func() error { return claudesettings.RemoveGitIgnored(st.Root, []string{rule}) })
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

	if err := m.selfCheck(token, shape); err != nil {
		return err
	}
	if shape == routing.WithoutProxy {
		fmt.Fprintln(io.Out, "Self-check passed: the resident process is up.")
	} else {
		fmt.Fprintln(io.Out, "Self-check passed: routing and recording verified end to end.")
	}
	fmt.Fprintln(io.Out, report.ShapeNotice(shape))
	fmt.Fprintln(io.Out, report.UnwitnessedReward)
	fmt.Fprintln(io.Out, contributesNow)
	fmt.Fprintln(io.Out, m.verdict(st.Root))
	return nil
}

// injectProject writes the project injection in the shape asked for:
// with the proxy base URL, or hooks alone. A file carrying the other
// shape is cleared first, through the one remover that knows what to
// put back — a base URL of the user's own that the injection displaced
// — because the shape without a base URL refuses to install over an
// injected one. enable and doctor both write through here, so a shape
// change is spelled once. The results are removal's: what was put back,
// and what could not be.
func (m *Machine) injectProject(st report.ProjectStatus, token string, shape routing.Shape) (restored, unrestored string, err error) {
	settingsPath := st.SettingsPath()
	baseURL := m.proxy.BaseURL(token)
	if shape == routing.WithoutProxy {
		baseURL = ""
	}
	if onFile, ok := claudesettings.InjectionShape(settingsPath); ok && onFile != shape {
		if restored, unrestored, err = m.removeInjection(st.Root); err != nil {
			return "", "", err
		}
	}
	return restored, unrestored, claudesettings.InjectProject(settingsPath, baseURL, projectHooks(m.deps.ExecPath))
}

// hookPolicy is the static reading of whether Claude Code will load the
// hooks a project injection installs on this device, taken fresh each
// time it is asked: the configuration it reads is the user's, or their
// organization's, and changes without notice.
func (m *Machine) hookPolicy(root string) claudesettings.HookPolicy {
	return claudesettings.JudgeHookPolicy(root, m.claude(), m.deps.Getenv)
}

// confirmHooksWillRun says what the static reading of the hooks means
// for this project, and reports whether enable goes on. Where another
// source still records it always does, and the outlook is the whole of
// what is said. Where the hooks are the only source an enable would
// install something that records nothing, so the one question enable
// adds is asked here, and a no leaves every file as it was.
func (m *Machine) confirmHooksWillRun(io IO, root string, shape routing.Shape) (bool, error) {
	outlook := report.ExplainHooks(m.hookPolicy(root), shape)
	if outlook.Runs {
		return true, nil
	}
	if !outlook.RecordsNothing {
		for _, line := range outlook.Lines() {
			fmt.Fprintln(io.Out, line)
		}
		return true, nil
	}
	fmt.Fprintln(io.Out, outlook.Judgement)
	fmt.Fprintln(io.Out, noProxyRecordsNothing)
	yes, err := askYesNo(io, "Enable anyway? [y/N] ", false)
	if err != nil {
		return false, fmt.Errorf("reading the answer: %w", err)
	}
	if !yes {
		fmt.Fprintln(io.Out, "Nothing was changed.")
	}
	return yes, nil
}

// registerEarlierSessions puts the session files found before the
// install into the project's registry. The registry goes on the ledger
// only when this install is the one creating it: a registry that
// already stood — from an earlier enable, or from a session's own
// hooks — is not this install's to take back.
//
// A file the reader retired keeps its entry and stays retired: enable
// finds it again, because it is still on disk, but running enable a
// second time is not an answer to why its reading stopped.
func (m *Machine) registerEarlierSessions(projectIDHash string, found discover.Result, ledger *enableLedger) error {
	projects, err := m.registry.Projects()
	if err != nil {
		return err
	}
	if !slices.Contains(projects, projectIDHash) {
		ledger.record(func() error { return m.registry.Unregister(projectIDHash) })
	}
	return discover.Register(m.registry, projectIDHash, found)
}

// confirmAgreement shows the agreement and records the explicit
// answer. An acceptance recorded for an older agreement version is
// stale: the terms changed, so the user must confirm again.
func (m *Machine) confirmAgreement(io IO) error {
	// Bytes that are not a consent record establish no acceptance, so
	// the agreement is shown in full and accepted again: this command
	// is what the unreadable-record pause tells the user to run, and it
	// must not stop at the same unreadable bytes. Every other failure
	// is a store that was never read — a permission denied, a disk
	// error — and a store nobody read says nothing about what was
	// accepted, so it must not be answered by asking again.
	version, _, err := m.consent.AcceptedVersion()
	switch {
	case errors.Is(err, consent.ErrUnreadable):
		version = ""
	case err != nil:
		return fmt.Errorf("reading the consent record at %s: %w", m.consent.Path(), err)
	}
	if version == consent.AgreementVersion {
		return nil
	}
	if version != "" {
		fmt.Fprintln(io.Out, "The data agreement changed since you last accepted it.")
	}
	fmt.Fprintln(io.Out, consent.AgreementText)
	fmt.Fprintln(io.Out)
	fmt.Fprintln(io.Out, deviceWideTerms)
	fmt.Fprintln(io.Out)
	yes, err := askYesNo(io, "Do you accept the data agreement? [yes/no]: ", false)
	if err != nil {
		return fmt.Errorf("reading agreement answer: %w", err)
	}
	if !yes {
		return ErrDeclined
	}
	if err := m.consent.AcceptAgreement(consent.AgreementVersion, m.now()); err != nil {
		return err
	}
	// Both consent pauses may resume now that the current terms are
	// accepted: the record is current and it is readable.
	if err := m.routes.Resume(routing.PauseConsentReconfirm); err != nil {
		return err
	}
	return m.routes.Resume(routing.PauseConsentUnreadable)
}

// projectToken reuses the active token when the project is already
// enabled — re-running enable must repair, not re-key — and mints a
// fresh 128-bit token otherwise.
func projectToken(st report.ProjectStatus) (string, error) {
	if st.Enabled {
		return st.Token, nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// selfCheck proves the injection works before enable reports success:
// the proxy is up and, when a base URL was injected, this exact token
// routes and records. Without a base URL there is no route to prove;
// what the hooks will bring up on every session is the resident process
// that uploads what this device records, and that it comes up is the
// check. No upstream call is made and nothing is billed.
func (m *Machine) selfCheck(token string, shape routing.Shape) error {
	if err := m.proxy.Ensure(); err != nil {
		if remedy := report.ProxyRemedy(err); remedy != "" {
			return fmt.Errorf("self-check failed: %v. %s", err, remedy)
		}
		return fmt.Errorf("self-check failed: %w", err)
	}
	if shape == routing.WithoutProxy {
		return nil
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
