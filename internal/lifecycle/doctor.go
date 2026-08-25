package lifecycle

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// findingSeverity orders what a doctor run found. Only problems count
// toward the exit code: repairs already happened and notes carry no
// action.
type findingSeverity int

const (
	findingOK findingSeverity = iota
	findingFixed
	findingNote
	findingProblem
)

func (s findingSeverity) label() string {
	switch s {
	case findingFixed:
		return "fixed"
	case findingNote:
		return "note"
	case findingProblem:
		return "problem"
	default:
		return "ok"
	}
}

// Finding is one fact a doctor run established, as a value: severity,
// the sentence, and any follow-up lines. The exit code derives from
// the severities, not from counting as text is printed.
type Finding struct {
	Severity findingSeverity
	Text     string
	Details  []string
}

// doctorReport accumulates findings.
type doctorReport struct {
	findings []Finding
}

func (r *doctorReport) add(severity findingSeverity, format string, a ...any) {
	r.findings = append(r.findings, Finding{Severity: severity, Text: fmt.Sprintf(format, a...)})
}

func (r *doctorReport) ok(format string, a ...any)    { r.add(findingOK, format, a...) }
func (r *doctorReport) fixed(format string, a ...any) { r.add(findingFixed, format, a...) }
func (r *doctorReport) problem(format string, a ...any) {
	r.add(findingProblem, format, a...)
}

// note reports something the user should read that doctor can neither
// verify nor fix, so it never counts toward the exit code.
func (r *doctorReport) note(format string, a ...any) { r.add(findingNote, format, a...) }

// detail attaches a follow-up line to the most recent finding.
func (r *doctorReport) detail(format string, a ...any) {
	last := &r.findings[len(r.findings)-1]
	last.Details = append(last.Details, fmt.Sprintf(format, a...))
}

// problems counts the findings the user still must act on.
func (r *doctorReport) problems() int {
	n := 0
	for _, f := range r.findings {
		if f.Severity == findingProblem {
			n++
		}
	}
	return n
}

func (r *doctorReport) render(out io.Writer) {
	for _, f := range r.findings {
		fmt.Fprintf(out, "  %s: %s\n", f.Severity.label(), f.Text)
		for _, d := range f.Details {
			fmt.Fprintf(out, "      %s\n", d)
		}
	}
}

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

	d, err := m.Diagnose(dir)
	if err != nil {
		return 0, err
	}
	r := &doctorReport{}
	doctorTokenStore(r, d.TokenStore)
	doctorPause(r, d.Project)
	m.doctorProxy(r, d)
	if err := m.doctorInjection(r, d.Project); err != nil {
		return 0, err
	}
	m.doctorDiscoveryHint(r, d.TokenStore)
	doctorSpool(r, d.Spool, m.deps.Layout.SpoolDir())
	doctorRejected(r, d.Rejected, d.RejectedErr, m.deps.Layout.RejectedDir())
	doctorService(r, d.Handshake, d.UpgradeMessage, m.deps.Version)
	doctorEnvironmentNote(r)
	m.doctorSelfcheck(r, d)

	r.render(io.Out)
	fmt.Fprintln(io.Out)
	if r.problems() == 0 {
		fmt.Fprintln(io.Out, "Everything checks out.")
	} else {
		fmt.Fprintf(io.Out, "%d problem(s) need attention.\n", r.problems())
	}
	return r.problems(), nil
}

// doctorTokenStore distinguishes signed-out from cannot-read: an
// unreadable token store leaves the pairing state unknown, and unknown
// must never present as the signed-out state.
func doctorTokenStore(r *doctorReport, ts TokenStoreState) {
	if ts.Err == nil {
		return
	}
	r.problem("the device token store could not be read: %v", ts.Err)
	r.detail("Pairing state is unknown; this is not the signed-out state. If the OS")
	r.detail("keyring is unavailable here, set %s=file and run `trajector login`.", tokenstore.BackendEnv)
}

// doctorPause reports a device-wide pause. Doctor never lifts one —
// signing in and reconfirming the agreement are the user's decisions —
// so this is always a problem with the lifting command named.
func doctorPause(r *doctorReport, st ProjectStatus) {
	if st.PauseReason == "" {
		return
	}
	r.problem("recording is paused everywhere: %s", st.PauseReason.Explain())
}

// doctorProxy checks who holds the proxy port. An unproven holder is
// the one finding doctor must shout about and can never repair:
// injected projects would send credentials to it. The verdict's reason
// decides the advice — only a proven stranger earns the stop-the-process
// remedy; an authentication failure may be the user's own proxy. Among
// our own proxies only a strictly older release is replaced; any other
// version keeps serving, or two coexisting builds would drain each
// other on every run.
func (m *Machine) doctorProxy(r *doctorReport, d Diagnosis) {
	switch d.Proxy.Holder {
	case proxylife.HolderOurs:
		h := d.Proxy.Health
		up := time.Duration(h.UptimeSeconds) * time.Second
		switch {
		case proxylife.Supersedes(m.deps.Version, h.Version):
			if err := m.proxy.Ensure(); err != nil {
				r.problem("a trajector proxy version %s holds %s and could not be replaced: %v", h.Version, d.Proxy.Addr, err)
				return
			}
			r.fixed("replaced the version %s proxy at %s with this build (%s)", h.Version, d.Proxy.Addr, m.deps.Version)
		case h.Version != m.deps.Version:
			r.ok("proxy running at %s (version %s, up %s); %s", d.Proxy.Addr, h.Version, up, proxylife.ReuseReason)
		default:
			r.ok("proxy running at %s (version %s, up %s)", d.Proxy.Addr, h.Version, up)
		}
	case proxylife.HolderForeign:
		r.problem("%v", d.Proxy.Reason)
		if remedy := ProxyRemedy(d.Proxy.Reason); remedy != "" {
			r.detail("%s", remedy)
		}
	default:
		if !d.Project.Enabled {
			r.ok("proxy not running; it starts on demand with the next session")
			return
		}
		if err := m.proxy.Ensure(); err != nil {
			r.problem("the proxy is down and could not be started: %v", err)
			return
		}
		r.fixed("started the capture proxy at %s", d.Proxy.Addr)
	}
}

// doctorInjection reconciles the project's injected settings with the
// routing table. The enable invariant — an injected base URL implies an
// active token and both hooks — is restored where the settings file
// alone is wrong; disagreements about consent are reported, never
// guessed at.
func (m *Machine) doctorInjection(r *doctorReport, st ProjectStatus) error {
	settingsPath := st.SettingsPath()
	switch {
	case !st.Enabled && !st.Injected():
		r.ok("this project is not enabled; nothing to reconcile")
		return nil

	case st.Enabled && !st.Injected():
		// Re-injecting would resume capture the user may have stopped on
		// purpose by hand-editing; consent questions are theirs to answer.
		r.problem("the routing table grants this project but its settings inject nothing.")
		r.detail("Run `trajector enable` to restore the injection, or `trajector disable`")
		r.detail("to withdraw this project.")
		return nil

	case !st.Enabled && st.Injected():
		restored, unrestored, err := m.removeInjection(st.Root)
		if err != nil {
			r.problem("a stale injection points traffic at a token that no longer records, and removing it failed: %v", err)
			return nil
		}
		r.fixed("removed a stale injection from %s (its token no longer records)", settingsPath)
		if restored != "" {
			r.detail("Put back the base URL of your own that the injection had displaced: %s", restored)
		}
		if unrestored != "" {
			r.problem("%s", unrestoredBaseURLWarning(settingsPath, unrestored))
		}
		return nil
	}

	if st.IdentityDisagreement() {
		r.problem("the routing table and the consent record disagree about this project's identity.")
		r.detail("Run `trajector disable` and then `trajector enable` to record both afresh.")
		return nil
	}
	if !st.Consistent() {
		if err := claudesettings.InjectProject(settingsPath, m.proxy.BaseURL(st.Token), hookCommand(m.deps.ExecPath, "ensure-proxy")); err != nil {
			return fmt.Errorf("repairing the injection in %s: %w", settingsPath, err)
		}
		r.fixed("rewrote the injection in %s (token and session hooks restored)", settingsPath)
	} else {
		r.ok("injection and routing agree for this project")
	}

	if action, err := claudesettings.EnsureGitIgnored(st.Root, claudesettings.ProjectLocalIgnoreRule); err != nil {
		r.problem("could not verify .gitignore covers %s: %v", claudesettings.ProjectLocalIgnoreRule, err)
	} else if action == claudesettings.IgnoreAppended {
		r.fixed("added %s to .gitignore", claudesettings.ProjectLocalIgnoreRule)
	} else if action == claudesettings.IgnoreSymlinked {
		r.problem(".gitignore is a symbolic link and was left alone; add %s to your git ignores yourself", claudesettings.ProjectLocalIgnoreRule)
	}

	m.doctorUpstream(r, st)
	return nil
}

// doctorUpstream reconciles the recorded upstream, the same self-heal
// the session hook performs, and reports what it did or could not do.
func (m *Machine) doctorUpstream(r *doctorReport, st ProjectStatus) {
	want, moved, refused, err := m.reconcileUpstream(st)
	switch {
	case want.unsupportedKey != "":
		r.problem("%s is set: Bedrock and Vertex channels are not supported, so this project's traffic is not being captured.", want.unsupportedKey)
	case refused:
		r.problem("this project's base-URL configuration moved to %s, which is refused: %s. The recorded upstream is unchanged.", want.upstream, nonLoopbackUpstreamRemedy)
	case err != nil:
		r.problem("this project's base URL moved to %s but the routing table could not be updated: %v", want.upstream, err)
	case moved:
		r.fixed("updated this project's upstream to %s (its base-URL configuration moved)", want.upstream)
	}
}

// doctorDiscoveryHint re-adds the user-level discovery hook a paired
// device is supposed to carry. Unpaired devices are left alone: login
// owns the first installation.
func (m *Machine) doctorDiscoveryHint(r *doctorReport, ts TokenStoreState) {
	if !ts.Paired {
		return
	}
	userSettings := claudesettings.UserSettingsPath(m.deps.Home)
	if claudesettings.HasHook(userSettings, claudesettings.DiscoveryMarker) {
		return
	}
	if err := claudesettings.InjectUserHook(userSettings, hookCommand(m.deps.ExecPath, "discovery")); err != nil {
		r.problem("the project-discovery hint is missing from %s and could not be re-added: %v", userSettings, err)
		return
	}
	r.fixed("re-added the project-discovery hint to %s", userSettings)
}

// doctorSpool verifies the capture spool accepts writes within quota.
func doctorSpool(r *doctorReport, s SpoolState, dir string) {
	if s.OpenErr != nil {
		r.problem("%s", spoolUnusableHeadline(dir, s.OpenErr))
		return
	}
	if s.WritableErr != nil {
		r.problem("%s", spoolUnwritableHeadline(s.WritableErr))
		r.detail("Spool: %s of %s used at %s.", platform.HumanBytes(s.Usage), platform.HumanBytes(s.Quota), dir)
		if s.Full() {
			r.detail("%s", spoolFullRemedy)
		}
		return
	}
	r.ok("capture spool writable (%s of %s used)", platform.HumanBytes(s.Usage), platform.HumanBytes(s.Quota))
}

// doctorRejected surfaces quarantined batches. They are never deleted
// or retried automatically — what happens to them is the user's call —
// so doctor lists each with its recorded reason and both commands that
// end its wait.
func doctorRejected(r *doctorReport, rejected []upload.RejectedBatch, listErr error, dir string) {
	if listErr != nil {
		r.problem("%s", rejectedUnreadableHeadline(dir, listErr))
		return
	}
	if len(rejected) == 0 {
		r.ok("no rejected batches quarantined")
		return
	}
	r.problem("%s:", quarantineHeadline(rejected))
	for _, b := range rejected {
		line := fmt.Sprintf("%s: %d rawcall(s)", b.BatchID, b.Records)
		if !b.Reason.At.IsZero() {
			line += ", rejected " + b.Reason.At.UTC().Format(time.RFC3339)
		}
		if b.Reason.Details != "" {
			line += " (" + b.Reason.Details + ")"
		}
		r.detail("%s", line)
	}
	r.detail("Run `trajector doctor requeue <batch-id>` (or `--all`) to upload them again,")
	r.detail("or `trajector doctor discard <batch-id>` (or `--all`) to delete them for good.")
}

// doctorService relays what the service last said, minus what this
// build has already answered. A minimum client version this build meets
// is left unsaid — see standing for why relaying it verbatim was worse
// than saying nothing. Nothing here is a problem: being behind the
// service is not a broken machine, and none of these lines move the
// exit code.
func doctorService(r *doctorReport, h platform.Handshake, upgradeMessage, version string) {
	v := standing(h.MinClientVersion, version)
	if v != versionSatisfied {
		r.note("the service requires client version %s or newer; this build is %s", h.MinClientVersion, version)
	}
	if upgradeMessage != "" {
		r.note("the service says: %s", upgradeMessage)
	}
	if v == versionBehind || upgradeMessage != "" {
		r.detail("%s", upgradeHint)
	}
	if h.Notice != "" {
		r.note("notice from the service: %s", h.Notice)
	}
}

// doctorSelfcheck closes an enabled project's diagnosis by asking the
// live proxy itself — the same self-check enable runs. Files can look
// right while the running proxy holds a state that will not record;
// only the proxy's own answer settles it.
func (m *Machine) doctorSelfcheck(r *doctorReport, d Diagnosis) {
	if !d.Project.Enabled || d.Proxy.Holder != proxylife.HolderOurs {
		return
	}
	if d.Selfcheck == nil {
		r.problem("the live proxy did not answer this project's self-check; run `trajector doctor` again once it settles")
		return
	}
	if err := m.explainSelfcheck(*d.Selfcheck); err != nil {
		r.problem("the files agree, but the live proxy will not record this project: %v", err)
		return
	}
	r.ok("live proxy confirms this project routes and records")
}

// quarantineHeadline is the one sentence both status and doctor use for
// quarantined batches, so the two surfaces cannot drift apart.
func quarantineHeadline(rejected []upload.RejectedBatch) string {
	records := 0
	for _, b := range rejected {
		records += b.Records
	}
	return fmt.Sprintf("%d rawcall(s) in %d rejected batch(es) are quarantined and will not be retried automatically", records, len(rejected))
}

// spoolUnusableHeadline is the one sentence both status and doctor use
// for a spool that could not be opened or read, so the two surfaces
// cannot drift apart.
func spoolUnusableHeadline(dir string, err error) string {
	return fmt.Sprintf("the capture spool at %s is not usable: %v", dir, err)
}

// spoolUnwritableHeadline is the one sentence both status and doctor use
// for a spool that opened but refuses writes. Every such refusal stops
// recording, whatever caused it, so both surfaces say so and name the
// cause the spool itself reported.
func spoolUnwritableHeadline(err error) string {
	return fmt.Sprintf("the capture spool is not writable, so recording is stopped: %v", err)
}

// spoolFullRemedy is the follow-up both surfaces print under the one
// writability refusal that has a way out of its own.
const spoolFullRemedy = "The spool is full. Run `trajector upload --force` to upload and free it."

// rejectedUnreadableHeadline is the one sentence both status and doctor
// use for a quarantine directory that could not be read.
func rejectedUnreadableHeadline(dir string, err error) string {
	return fmt.Sprintf("the rejected batches at %s could not be read: %v", dir, err)
}

// doctorEnvironmentNote points out platform topologies that need care.
// These are informational: there is no reliable signal to check against,
// so nothing here counts as a problem.
func doctorEnvironmentNote(r *doctorReport) {
	if runtime.GOOS == "linux" && isWSL() {
		r.ok("running under WSL: Claude Code must also run inside WSL; a Windows-native claude cannot reach this trajector")
	}
	if runtime.GOOS == "windows" {
		r.ok("Windows: if a firewall prompt appears for the proxy, allow loopback access; the proxy only ever binds 127.0.0.1")
	}
}

// isWSL detects Windows Subsystem for Linux by its kernel signature.
func isWSL() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), "microsoft")
}
