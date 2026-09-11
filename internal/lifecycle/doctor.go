package lifecycle

import (
	"fmt"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// Doctor diagnoses the device and the current project, repairs what is
// safely its own to repair — injected settings, hooks, the recorded
// upstream — and reports everything else with the command that resolves
// it. It returns how many problems remain unresolved. Repairs only ever
// touch state trajector itself wrote, and every repair is idempotent:
// running doctor twice is always safe.
func (m *Machine) Doctor(dir string, io IO) (problems int, err error) {
	fmt.Fprintf(io.Out, "trajector %s doctor\n\n", m.deps.Version)

	// An upgrade that could not delete the binary it replaced — on
	// Windows the previous one is still the running image — leaves a
	// file beside this one. It is housekeeping, not a diagnosis: the
	// user never had to know, so it is swept without a finding.
	selfupdate.SweepResidue(m.deps.ExecPath)

	resumed, pausedBy, err := m.resumeRedactionPauseAfterUpgrade()
	if err != nil {
		return 0, err
	}
	d, err := m.Diagnose(dir)
	if err != nil {
		return 0, err
	}
	// The findings a diagnosis alone establishes are the renderer's; the
	// ones between them are this machine's repairs, and the order they
	// are written in is the order the user reads them.
	f := &report.Findings{}
	if resumed {
		f.Fixed("recording resumed after upgrade: it was paused by version %s, and this build is %s", pausedBy, m.deps.Version)
		f.Detail("Session files are read again from where reading stopped.")
	}
	report.DoctorDevice(f, d)
	m.doctorProxy(f, d)
	if err := m.doctorInjection(f, d.Project); err != nil {
		return 0, err
	}
	m.doctorDiscoveryHint(f, d.TokenStore)
	m.doctorSessionFiles(f, d)
	report.DoctorData(f, d)
	report.DoctorEnvironment(f)
	m.doctorSelfcheck(f, d)

	f.Render(io.Out)
	fmt.Fprintln(io.Out)
	if f.Problems() == 0 {
		fmt.Fprintln(io.Out, "Everything checks out.")
	} else {
		fmt.Fprintf(io.Out, "%d problem(s) need attention.\n", f.Problems())
	}
	return f.Problems(), nil
}

// resumeRedactionPauseAfterUpgrade lifts the pause a build set when
// session records took a shape its redaction did not cover, once a
// different build is running: the pause exists to wait for a build
// that covers the shape, and this is the first command that runs under
// one. The same build lifts nothing — it would meet the same lines
// again — and the pause stays reported as it is. A pause with no build
// recorded was set by a build that is not this one, and is lifted the
// same way. It returns what build the pause named.
func (m *Machine) resumeRedactionPauseAfterUpgrade() (resumed bool, pausedBy string, err error) {
	reason, err := m.routes.PausedReason()
	if err != nil || reason != routing.PauseRedactionDrift {
		return false, "", err
	}
	pausedBy, err = m.routes.PausedByBuild()
	if err != nil || pausedBy == m.deps.Version {
		return false, pausedBy, err
	}
	if err := m.routes.Resume(routing.PauseRedactionDrift); err != nil {
		return false, pausedBy, err
	}
	if pausedBy == "" {
		pausedBy = "an earlier build"
	}
	return true, pausedBy, nil
}

// doctorProxy checks who holds the proxy port. An unproven holder is
// the one finding doctor must shout about and can never repair:
// injected projects would send credentials to it. The verdict's reason
// decides the advice — only a proven stranger earns the stop-the-process
// remedy; an authentication failure may be the user's own proxy. Which
// of our own proxies doctor replaces and which it leaves alone is the
// verdict's own answer, never a version comparison made here.
func (m *Machine) doctorProxy(f *report.Findings, d report.Diagnosis) {
	h := d.Proxy.Health
	up := time.Duration(h.UptimeSeconds) * time.Second
	switch {
	case d.Proxy.Replaceable(m.deps.Version):
		if err := m.proxy.Ensure(); err != nil {
			f.Problem("a trajector proxy version %s holds %s and could not be replaced: %v", h.Version, d.Proxy.Addr, err)
			return
		}
		f.Fixed("replaced the version %s proxy at %s with this build (%s)", h.Version, d.Proxy.Addr, m.deps.Version)
	case d.Proxy.Serving(m.deps.Version):
		if h.Version != m.deps.Version {
			f.OK("proxy running at %s (version %s, up %s); %s", d.Proxy.Addr, h.Version, up, proxylife.ReuseReason)
			return
		}
		f.OK("proxy running at %s (version %s, up %s)", d.Proxy.Addr, h.Version, up)
	case d.Proxy.Holder == proxylife.HolderForeign:
		f.Problem("%v", d.Proxy.Reason)
		if remedy := report.ProxyRemedy(d.Proxy.Reason); remedy != "" {
			f.Detail("%s", remedy)
		}
	default:
		if !d.Project.Enabled {
			f.OK("proxy not running; it starts on demand with the next session")
			return
		}
		if d.ProxyIdleBetweenSessions {
			// Nothing to start: the resident process comes up with the
			// next session, and a device where no project routes through
			// it has no traffic waiting on it in between.
			f.OK("proxy not running; on this device it runs only while a session is open, because every enabled project records without it")
			return
		}
		if err := m.proxy.Ensure(); err != nil {
			f.Problem("the proxy is down and could not be started: %v", err)
			return
		}
		f.Fixed("started the capture proxy at %s", d.Proxy.Addr)
	}
}

// doctorInjection reconciles the project's injected settings with the
// routing table. The enable invariant — an injected base URL implies an
// active token and both hooks — is restored where the settings file
// alone is wrong; disagreements about consent are reported, never
// guessed at.
func (m *Machine) doctorInjection(f *report.Findings, st report.ProjectStatus) error {
	settingsPath := st.SettingsPath()
	switch {
	case !st.Enabled && !st.Injected():
		f.OK("this project is not enabled; nothing to reconcile")
		return nil

	case st.Enabled && !st.Injected():
		// Re-injecting would resume capture the user may have stopped on
		// purpose by hand-editing; consent questions are theirs to answer.
		f.Problem("the routing table grants this project but its settings inject nothing.")
		f.Detail("Run `trajector enable` to restore the injection, or `trajector disable`")
		f.Detail("to withdraw this project.")
		return nil

	case !st.Enabled && st.Injected():
		restored, unrestored, err := m.removeInjection(st.Root)
		if err != nil {
			f.Problem("a stale injection points traffic at a token that no longer records, and removing it failed: %v", err)
			return nil
		}
		f.Fixed("removed a stale injection from %s (its token no longer records)", settingsPath)
		if restored != "" {
			f.Detail("Put back the base URL of your own that the injection had displaced: %s", restored)
		}
		if unrestored != "" {
			f.Problem("%s", unrestoredBaseURLWarning(settingsPath, unrestored))
		}
		undone, failures := m.restoreRecordedSettings(st.Root, st.Hash, claudesettings.OptionalSettings)
		for _, key := range undone {
			f.Detail("%s", settingRestoredLine(key))
		}
		for _, err := range failures {
			f.Problem("%v", err)
		}
		return nil
	}

	if st.IdentityDisagreement() {
		f.Problem("the routing table and the consent record disagree about this project's identity.")
		f.Detail("Run `trajector disable` and then `trajector enable` to record both afresh.")
		return nil
	}
	if !st.Consistent() {
		// The file is rewritten in the shape the grant records: the shape
		// was the user's choice at enable time, and the grant is where
		// enable wrote that choice down.
		restored, unrestored, err := m.injectProject(st, st.Token, st.GrantNoProxy)
		if err != nil {
			return fmt.Errorf("repairing the injection in %s: %w", settingsPath, err)
		}
		f.Fixed("rewrote the injection in %s (token and session hooks restored)", settingsPath)
		if restored != "" {
			f.Detail("Put back the base URL of your own that the injection had displaced: %s", restored)
		}
		if unrestored != "" {
			f.Problem("%s", unrestoredBaseURLWarning(settingsPath, unrestored))
		}
	} else {
		f.OK("injection and routing agree for this project")
	}

	if action, err := claudesettings.EnsureGitIgnored(st.Root, claudesettings.ProjectLocalIgnoreRule); err != nil {
		f.Problem("could not verify .gitignore covers %s: %v", claudesettings.ProjectLocalIgnoreRule, err)
	} else if action == claudesettings.IgnoreAppended {
		f.Fixed("added %s to .gitignore", claudesettings.ProjectLocalIgnoreRule)
	} else if action == claudesettings.IgnoreSymlinked {
		f.Problem(".gitignore is a symbolic link and was left alone; add %s to your git ignores yourself", claudesettings.ProjectLocalIgnoreRule)
	}

	m.doctorUpstream(f, st)
	return nil
}

// doctorUpstream reconciles the recorded upstream, the same self-heal
// the session hook performs, and reports what it did or could not do.
func (m *Machine) doctorUpstream(f *report.Findings, st report.ProjectStatus) {
	want, moved, refused, err := m.reconcileUpstream(st)
	switch {
	case want.unsupportedKey != "":
		f.Problem("%s is set: Bedrock and Vertex channels are not supported, so this project's traffic is not being captured.", want.unsupportedKey)
	case refused:
		f.Problem("this project's base-URL configuration moved to %s, which is refused: %s. The recorded upstream is unchanged.", want.upstream, nonLoopbackUpstreamRemedy)
	case err != nil:
		f.Problem("this project's base URL moved to %s but the routing table could not be updated: %v", want.upstream, err)
	case moved:
		f.Fixed("updated this project's upstream to %s (its base-URL configuration moved)", want.upstream)
	}
}

// doctorDiscoveryHint re-adds the user-level discovery hook a paired
// device is supposed to carry. Unpaired devices are left alone: login
// owns the first installation.
func (m *Machine) doctorDiscoveryHint(f *report.Findings, ts report.TokenStoreState) {
	if !ts.Paired {
		return
	}
	userSettings := claudesettings.UserSettingsPath(m.deps.Home)
	if claudesettings.HasHook(userSettings, claudesettings.DiscoveryMarker) {
		return
	}
	if err := claudesettings.InjectUserHook(userSettings, hookCommand(m.deps.ExecPath, claudesettings.HookDiscovery)); err != nil {
		f.Problem("the project-discovery hint is missing from %s and could not be re-added: %v", userSettings, err)
		return
	}
	f.Fixed("re-added the project-discovery hint to %s", userSettings)
}

// doctorSelfcheck closes an enabled project's diagnosis by asking the
// live proxy itself — the same self-check enable runs. Files can look
// right while the running proxy holds a state that will not record;
// only the proxy's own answer settles it.
func (m *Machine) doctorSelfcheck(f *report.Findings, d report.Diagnosis) {
	if !d.Project.Enabled || d.Proxy.Holder != proxylife.HolderOurs {
		return
	}
	if d.Selfcheck == nil {
		f.Problem("the live proxy did not answer this project's self-check; run `trajector doctor` again once it settles")
		return
	}
	if err := m.explainSelfcheck(*d.Selfcheck); err != nil {
		f.Problem("the files agree, but the live proxy will not record this project: %v", err)
		return
	}
	f.OK("live proxy confirms this project routes and records")
}

// doctorSessionFiles looks for the current project's session files on
// disk and holds them against the registry, then hands what it found
// to the renderer. It registers nothing: registering is enable's and
// the session hooks' alone, and a doctor run must leave the state it
// diagnosed as it found it. A project Claude Code opens from the
// Windows side is not searched: its session files are on that side,
// where this process cannot reach, and the diagnosis already says so.
func (m *Machine) doctorSessionFiles(f *report.Findings, d report.Diagnosis) {
	st := d.Project
	if !st.Enabled {
		return
	}
	if st.WindowsSideClaude || d.SessionFiles.Err != nil {
		report.DoctorProject(f, d, report.Discovery{})
		return
	}
	report.DoctorProject(f, d, m.discoverSessionFiles(st))
}

// discoverSessionFiles walks the project's tree once and counts the
// sessions found there that the registry does not hold.
func (m *Machine) discoverSessionFiles(st report.ProjectStatus) report.Discovery {
	found, err := discover.Walk(st.Root, m.claudeConfigDir())
	if err != nil {
		return report.Discovery{Err: err}
	}
	files := m.sessionFiles(st.Hash).Files
	registered := make(map[string]bool, len(files))
	for _, f := range files {
		registered[f.Path] = true
	}
	disc := report.Discovery{Gaps: found.Gaps()}
	for _, path := range found.Files {
		if (follow.File{Path: path}).MainSession() && !registered[path] {
			disc.Unregistered++
		}
	}
	return disc
}
