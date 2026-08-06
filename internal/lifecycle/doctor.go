package lifecycle

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// doctorReport collects what a doctor run found and did. Repairs are
// not problems: only findings the user must act on keep the exit code
// nonzero.
type doctorReport struct {
	io       IO
	problems int
}

func (r *doctorReport) ok(format string, a ...any) {
	fmt.Fprintf(r.io.Out, "  ok: "+format+"\n", a...)
}

func (r *doctorReport) fixed(format string, a ...any) {
	fmt.Fprintf(r.io.Out, "  fixed: "+format+"\n", a...)
}

func (r *doctorReport) problem(format string, a ...any) {
	r.problems++
	fmt.Fprintf(r.io.Out, "  problem: "+format+"\n", a...)
}

func (r *doctorReport) detail(format string, a ...any) {
	fmt.Fprintf(r.io.Out, "      "+format+"\n", a...)
}

// note reports something the user should read that doctor can neither
// verify nor fix, so it never counts toward the exit code.
func (r *doctorReport) note(format string, a ...any) {
	fmt.Fprintf(r.io.Out, "  note: "+format+"\n", a...)
}

// Doctor diagnoses the device and the current project, repairs what is
// safely its own to repair — injected settings, hooks, the recorded
// upstream — and reports everything else with the command that resolves
// it. It returns how many problems remain unresolved. Repairs only ever
// touch state trajector itself wrote, and every repair is idempotent:
// running doctor twice is always safe.
func (m *Machine) Doctor(dir string, io IO) (problems int, err error) {
	fmt.Fprintf(io.Out, "trajector %s doctor\n\n", m.deps.Version)
	r := &doctorReport{io: io}

	st, err := m.Project(dir)
	if err != nil {
		return 0, err
	}
	m.doctorProxy(r, st)
	if err := m.doctorInjection(r, st); err != nil {
		return 0, err
	}
	m.doctorDiscoveryHint(r)
	if err := m.doctorSpool(r); err != nil {
		return 0, err
	}
	if err := m.doctorRejected(r); err != nil {
		return 0, err
	}
	m.doctorService(r)
	doctorEnvironmentNote(r)

	fmt.Fprintln(io.Out)
	if r.problems == 0 {
		fmt.Fprintln(io.Out, "Everything checks out.")
	} else {
		fmt.Fprintf(io.Out, "%d problem(s) need attention.\n", r.problems)
	}
	return r.problems, nil
}

// doctorProxy checks who holds the proxy port. A foreign holder is the
// one finding doctor must shout about and can never repair: injected
// projects would send credentials to it.
func (m *Machine) doctorProxy(r *doctorReport, st ProjectStatus) {
	h, running := m.proxy.Health()
	switch {
	case running && h.Service == apiproxy.ServiceName:
		if h.Version != m.deps.Version {
			if err := m.proxy.Ensure(); err != nil {
				r.problem("a trajector proxy version %s holds %s and could not be replaced: %v", h.Version, m.proxy.Addr(), err)
				return
			}
			r.fixed("replaced the version %s proxy at %s with this build (%s)", h.Version, m.proxy.Addr(), m.deps.Version)
			return
		}
		r.ok("proxy running at %s (version %s, up %s)", m.proxy.Addr(), h.Version, time.Duration(h.UptimeSeconds)*time.Second)
	case running:
		r.problem("%s is held by a process that is not the trajector proxy.", m.proxy.Addr())
		r.detail("Enabled projects route API credentials at this address. Find and stop the")
		r.detail("process holding the port, or run `trajector disable` in enabled projects.")
	default:
		if !st.Enabled {
			r.ok("proxy not running; it starts on demand with the next session")
			return
		}
		if err := m.proxy.Ensure(); err != nil {
			r.problem("the proxy is down and could not be started: %v", err)
			return
		}
		r.fixed("started the capture proxy at %s", m.proxy.Addr())
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
		if err := claudesettings.RemoveProject(settingsPath); err != nil {
			r.problem("a stale injection points traffic at a token that no longer records, and removing it failed: %v", err)
			return nil
		}
		r.fixed("removed a stale injection from %s (its token no longer records)", settingsPath)
		return nil
	}

	if st.IdentityDisagreement() {
		r.problem("the routing table and the consent record disagree about this project's identity.")
		r.detail("Run `trajector disable` and then `trajector enable` to rebuild both.")
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

	if action, err := claudesettings.EnsureGitIgnored(st.Root, claudesettings.ProjectLocalRel); err != nil {
		r.problem("could not verify .gitignore covers %s: %v", claudesettings.ProjectLocalRel, err)
	} else if action == claudesettings.IgnoreAppended {
		r.fixed("added %s to .gitignore", claudesettings.ProjectLocalRel)
	}

	m.doctorUpstream(r, st)
	return nil
}

// doctorUpstream reconciles the recorded upstream, the same self-heal
// the session hook performs, and reports what it did or could not do.
func (m *Machine) doctorUpstream(r *doctorReport, st ProjectStatus) {
	want, moved, err := m.reconcileUpstream(st.Root, st.Upstream)
	switch {
	case want.unsupportedKey != "":
		r.problem("%s is set: Bedrock and Vertex channels are not supported, so this project's traffic is not being captured.", want.unsupportedKey)
	case err != nil:
		r.problem("this project's base URL moved to %s but the routing table could not be updated: %v", want.upstream, err)
	case moved:
		r.fixed("updated this project's upstream to %s (its base-URL configuration moved)", want.upstream)
	}
}

// doctorDiscoveryHint re-adds the user-level discovery hook a paired
// device is supposed to carry. Unpaired devices are left alone: login
// owns the first installation.
func (m *Machine) doctorDiscoveryHint(r *doctorReport) {
	if !m.Paired() {
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
func (m *Machine) doctorSpool(r *doctorReport) error {
	quota := upload.LoadHandshake(m.deps.Layout.UploadDir()).SpoolQuotaBytes
	sp, err := spool.Create(m.deps.Layout.SpoolDir(), quota)
	if err != nil {
		r.problem("the capture spool at %s is not usable: %v", m.deps.Layout.SpoolDir(), err)
		return nil
	}
	if err := sp.Writable(); err != nil {
		r.problem("the capture spool is not writable, so recording is stopped: %v", err)
		r.detail("Spool: %s of %s used at %s.", humanBytes(sp.Usage()), humanBytes(sp.Quota()), m.deps.Layout.SpoolDir())
		if sp.Usage() >= sp.Quota() {
			r.detail("The spool is full. Run `trajector upload --force` to upload and free it.")
		}
		return nil
	}
	r.ok("capture spool writable (%s of %s used)", humanBytes(sp.Usage()), humanBytes(sp.Quota()))
	return nil
}

// doctorRejected surfaces quarantined batches. They are never deleted
// or retried automatically — what happens to them is the user's call —
// so doctor lists each with its recorded reason and the command that
// requeues it.
func (m *Machine) doctorRejected(r *doctorReport) error {
	rejected, err := upload.ListRejected(m.deps.Layout.RejectedDir())
	if err != nil {
		return err
	}
	if len(rejected) == 0 {
		r.ok("no rejected batches quarantined")
		return nil
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
	r.detail("or `trajector disable` in the project to delete its local data.")
	return nil
}

// doctorService relays what the service last said. The client never
// parses the version — it cannot judge whether this build satisfies it
// — so both fields are shown verbatim and count toward nothing.
func (m *Machine) doctorService(r *doctorReport) {
	h := upload.LoadHandshake(m.deps.Layout.UploadDir())
	if h.MinClientVersion != "" {
		r.note("the service requires client version %s or newer; this build is %s", h.MinClientVersion, m.deps.Version)
	}
	if h.Notice != "" {
		r.note("notice from the service: %s", h.Notice)
	}
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
