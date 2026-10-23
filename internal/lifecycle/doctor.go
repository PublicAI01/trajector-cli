package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
	"github.com/PublicAI01/trajector-cli/internal/sessionread"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// Doctor diagnoses the device and the current project, repairs what is
// safely its own to repair — injected settings, hooks, the recorded
// upstream — and reports everything else with the command that resolves
// it. It returns how many problems remain unresolved. Repairs only ever
// touch state trajector itself wrote, and every repair is idempotent:
// running doctor twice is always safe.
func (m *Machine) Doctor(ctx context.Context, dir string, streams IO) (problems int, err error) {
	fmt.Fprintf(streams.Out, "trajector %s doctor\n\n", m.deps.Version)
	f := &report.Findings{}
	// Doctor is the one command that pays for the second reading: it
	// acts on the session files no hook reported, and it repairs from
	// the same value it reports.
	d, err := m.doctorOpen(ctx, dir, fromTree, f)
	if err != nil {
		return 0, err
	}
	run := doctorRun{f: f, d: &d, startProxy: d.Project.Enabled, reportedPause: d.Project.Pause()}
	if err := m.runDoctorChecks(ctx, run, devicePart, projectPart); err != nil {
		return 0, err
	}
	f.Render(streams.Out, streams.OutStyle)
	return doctorVerdict(streams, f.Problems()), nil
}

// DoctorAll is Doctor for every project this device records: the
// device's part once, then for each project a standing grant covers the
// whole of the project's part — injection, .gitignore, upstream, the
// session files and the live self-check — under the project's root. A
// root that is gone is one line saying so, never a problem: the grant
// stands, and nothing there can be checked or repaired. It is the
// command a notice sends the user to when something on the device
// needs more than the session hooks repair.
func (m *Machine) DoctorAll(ctx context.Context, dir string, streams IO) (problems int, err error) {
	fmt.Fprintf(streams.Out, "trajector %s doctor\n", m.deps.Version)
	device := &report.Findings{}
	// The device's part reads no project's tree: what it reports about
	// the current directory is device-wide, and each project's own
	// reading is paid for in its own part.
	d, err := m.doctorOpen(ctx, dir, fromRegistry, device)
	if err != nil {
		return 0, err
	}
	roots, err := m.grantedRoots()
	if err != nil {
		return 0, err
	}
	// Only a project still there has sessions that need the proxy.
	wanted := slices.ContainsFunc(roots, func(root string) bool { return !rootGone(root) })
	if err := m.doctorDevice(ctx, doctorRun{f: device, d: &d, startProxy: wanted}); err != nil {
		return 0, err
	}
	device.RenderSection(streams.Out, streams.OutStyle, report.DoctorDeviceHeading)
	problems = device.Problems()
	for _, root := range roots {
		f := &report.Findings{}
		if err := m.doctorProject(ctx, f, root, d.Project.Pause()); err != nil {
			report.DoctorProjectFailed(f, err)
		}
		f.RenderSection(streams.Out, streams.OutStyle, root)
		problems += f.Problems()
	}
	return doctorVerdict(streams, problems), nil
}

// doctorVerdict closes a doctor run with the one line a user reads
// first, and returns the count it states.
func doctorVerdict(streams IO, problems int) int {
	fmt.Fprintln(streams.Out)
	if problems == 0 {
		fmt.Fprintln(streams.Out, "Everything checks out.")
	} else {
		fmt.Fprintf(streams.Out, "%d problem(s) need attention.\n", problems)
	}
	return problems
}

// doctorOpen does what a doctor run does before any check, once per
// run however many projects it covers, and returns the diagnosis of dir
// the device's checks repair from.
func (m *Machine) doctorOpen(ctx context.Context, dir string, reading sessionFileReading, f *report.Findings) (report.Diagnosis, error) {
	// An upgrade leaves files beside this binary when it cannot finish
	// cleaning up: on Windows the binary it replaced, which is still the
	// running image, and anywhere the binary it kept while the build it
	// put in place had not yet accepted the install. The sweep removes
	// the first and puts the second back in place of the unaccepted
	// build, so after it this process may no longer be the installed
	// build; the diagnosis below is still this process's own. It is
	// housekeeping, not a diagnosis, so it is swept without a finding.
	// While an upgrade is running the files beside the binary are its
	// own, and the sweep leaves them to it.
	selfupdate.SweepResidue(m.installation())

	// A pause that waits for a build other than the one that set it is
	// lifted here: doctor is the command the user is told to run once
	// the upgrade that brings such a build is installed. It is lifted
	// before the diagnosis so that one run never reports a pause it has
	// itself already lifted.
	//
	// A routing table that cannot be read holds no pause this run can
	// read or lift. That is not a reason to stop: the diagnosis below
	// reports the table, and doctor is where the user looks for it.
	resumed, pausedBy, err := m.routes.ResumeOtherBuild(routing.PauseRedactionDrift, m.deps.Version)
	_, tableUnreadable := errors.AsType[*routing.UnreadableError](err)
	if err != nil && !tableUnreadable {
		return report.Diagnosis{}, err
	}
	// A pause this build set over a line read without its newline is
	// lifted here too, without waiting for another build, when this
	// build reads the same files whole now. Nothing else lifts it, so a
	// device whose reason is gone would otherwise record nothing until
	// an upgrade arrived.
	rescanned := false
	if !resumed && !tableUnreadable {
		rescanned, err = m.resumeAfterCleanRescan()
		if err != nil {
			return report.Diagnosis{}, err
		}
	}
	d, err := m.diagnose(ctx, dir, reading)
	if err != nil {
		return d, err
	}
	// The findings a diagnosis alone establishes are the renderer's; the
	// ones between them are this machine's repairs, and the order they
	// are written in is the order the user reads them.
	if rescanned {
		f.Fixedf("recording resumed: this build read the session files again and every line it read ended in a newline")
		f.Detailf("Session files are read again from where reading stopped.")
	}
	if resumed {
		f.Fixedf("recording resumed after upgrade: it was paused by %s, and this build is %s", pausedBy, m.deps.Version)
		f.Detailf("Session files are read again from where reading stopped.")
	}
	// Before anything is rendered, so the count the data section
	// prints is what is still held after this run.
	d.Spool.Held = m.releaseHeldSegments(f)
	return d, nil
}

// doctorPart is which part of a doctor run a check belongs to: the
// device's, said once a run, or a project's, said once for each project
// the run covers.
type doctorPart int

const (
	devicePart doctorPart = iota
	projectPart
)

// doctorRun is what one part of a doctor run checks and writes to: the
// findings it adds to, the diagnosis it repairs from, and whether the
// proxy is wanted running now because a project the run covers is
// enabled. A proxy no covered project wants is left to start on
// demand. reportedPause is the device-wide pause the device part of
// the run reported, empty when it reported none: a project's part reads
// its own diagnosis later, and a pause written in between is not one
// the device part said.
type doctorRun struct {
	f             *report.Findings
	d             *report.Diagnosis
	startProxy    bool
	reportedPause routing.PauseReason
}

// doctorCheck is one step of a doctor run.
type doctorCheck struct {
	part doctorPart
	run  func(ctx context.Context, m *Machine, r doctorRun) error
}

// doctorChecks is every step of a doctor run, in the order a run about
// one project says them. The device's steps and the project's
// alternate because that is the order the plain report is read in; a
// run about every project says the device's steps once, in this order,
// and then each project's.
var doctorChecks = []doctorCheck{
	{devicePart, func(_ context.Context, _ *Machine, r doctorRun) error {
		report.DoctorDevice(r.f, *r.d)
		return nil
	}},
	{devicePart, func(ctx context.Context, m *Machine, r doctorRun) error {
		m.doctorProxy(ctx, r.f, *r.d, r.startProxy)
		return nil
	}},
	{projectPart, func(ctx context.Context, m *Machine, r doctorRun) error {
		return m.doctorInjection(ctx, r.f, r.d.Project)
	}},
	{devicePart, func(_ context.Context, m *Machine, r doctorRun) error {
		m.doctorDiscoveryHint(r.f, r.d.TokenStore)
		return nil
	}},
	{devicePart, func(_ context.Context, m *Machine, r doctorRun) error {
		m.doctorStaleDiscoveryHook(r.f, *r.d)
		return nil
	}},
	{projectPart, func(ctx context.Context, m *Machine, r doctorRun) error {
		m.doctorEarlierSessions(ctx, r.f, r.d)
		return nil
	}},
	{projectPart, func(_ context.Context, _ *Machine, r doctorRun) error {
		report.DoctorProject(r.f, *r.d)
		return nil
	}},
	{devicePart, func(_ context.Context, _ *Machine, r doctorRun) error {
		report.DoctorData(r.f, *r.d)
		return nil
	}},
	{devicePart, func(_ context.Context, _ *Machine, r doctorRun) error {
		report.DoctorEnvironment(r.f)
		return nil
	}},
	{projectPart, func(_ context.Context, m *Machine, r doctorRun) error {
		m.doctorSelfcheck(r.f, *r.d, r.reportedPause)
		return nil
	}},
}

// runDoctorChecks runs the steps of the parts named, in doctorChecks
// order, and stops at the first that fails.
func (m *Machine) runDoctorChecks(ctx context.Context, r doctorRun, parts ...doctorPart) error {
	for _, c := range doctorChecks {
		if !slices.Contains(parts, c.part) {
			continue
		}
		if err := c.run(ctx, m, r); err != nil {
			return err
		}
	}
	return nil
}

// doctorDevice is the device's part of a doctor run: pairing, the
// routing table, a pause, automatic upgrades, the proxy, the discovery
// hooks, the captured data and the platform.
func (m *Machine) doctorDevice(ctx context.Context, r doctorRun) error {
	return m.runDoctorChecks(ctx, r, devicePart)
}

// doctorProject is one project's part of a doctor run, over a
// diagnosis of dir of its own, read in full. reportedPause is the pause
// the device part reported. A root that is no longer there is said in
// one line and checked no further.
func (m *Machine) doctorProject(ctx context.Context, f *report.Findings, dir string, reportedPause routing.PauseReason) error {
	if rootGone(dir) {
		report.DoctorProjectGone(f)
		return nil
	}
	d, err := m.diagnose(ctx, dir, fromTree)
	if err != nil {
		return err
	}
	return m.runDoctorChecks(ctx, doctorRun{f: f, d: &d, reportedPause: reportedPause}, projectPart)
}

// grantedRoots is every project root a standing grant covers, each
// once, sorted, as Uninstall lists them. A routing table that cannot
// be read lists none: the device's part already reports the table, and
// no project's part could be checked against it.
func (m *Machine) grantedRoots() ([]string, error) {
	grants, err := m.routes.All()
	if _, ok := errors.AsType[*routing.UnreadableError](err); ok {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	roots := map[string]bool{}
	for _, g := range grants {
		if !g.Revoked && g.RootPath != "" {
			roots[g.RootPath] = true
		}
	}
	return slices.Sorted(maps.Keys(roots)), nil
}

// rootGone reports that a granted project root is no longer a
// directory: nothing is there, or something that is not a directory
// is. Such a root has no settings file to complete or check, and the
// grant on it still stands. A root that cannot be looked at for any
// other reason is still there, and the step that reads it says what
// fails.
func rootGone(root string) bool {
	info, err := os.Stat(root)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	return !info.IsDir()
}

// resumeAfterCleanRescan lifts a redaction pause this build itself
// set, when reading the files again finds nothing this build must
// stop at. The pause is the device's, so every project's hot files
// are read, and one file that still stops reading keeps the pause for
// all of them. Nothing is stored and no cursor moves: the question is
// only whether the reason still holds.
func (m *Machine) resumeAfterCleanRescan() (bool, error) {
	reason, err := m.routes.PausedReason()
	if err != nil || reason != routing.PauseRedactionDrift {
		return false, err
	}
	grants, err := m.routes.All()
	if err != nil {
		return false, err
	}
	sp, err := m.spool()
	if err != nil {
		return false, err
	}
	reader := m.reader(sp)
	now := m.deps.Now()
	for _, g := range grants {
		if !g.Revoked && m.stillStops(reader, g, now) {
			return false, nil
		}
	}
	if err := m.routes.Resume(routing.PauseRedactionDrift); err != nil {
		return false, err
	}
	return true, nil
}

// stillStops reports whether a hot file of g's project still stops
// reading. A registry that cannot be read cannot show that the reason
// is gone, so it answers yes and the pause stays.
func (m *Machine) stillStops(reader sessionread.Reader, g routing.Grant, now time.Time) bool {
	files, err := m.registry.Files(g.ProjectIDHash)
	if err != nil {
		return true
	}
	hot, _ := follow.Split(files, now, follow.ProcessAlive)
	return reader.Stops(sessionread.ProjectOf(g), hot)
}

// releaseHeldSegments offers the segments kept on this machine to the
// reader again, project by project, and reports what came of it.
// Doctor is where that offer is made, because it is the command a user
// runs after an upgrade, and a build whose anchored list covers a
// shape an earlier build did not is how a held segment reaches the
// service at last.
//
// Which build's judgement applies is the reader's alone: the machine
// only says which project's files a held record came from, by the
// project id hash the record carries. A record no standing grant
// accounts for is offered to no one — without the project's root
// nothing here can state where its session ran, and a release decided
// on less than the reading knew would upload what the reading kept
// back. A withdrawn project's records are left alone for the same
// reason consent withdrawal deletes them: they are not going anywhere.
func (m *Machine) releaseHeldSegments(f *report.Findings) report.HeldRecords {
	sp, err := m.spool()
	if err != nil {
		return report.HeldRecordsOf(nil)
	}
	held, err := sp.Held()
	if err != nil || len(held) == 0 {
		return report.HeldRecordsOf(nil)
	}
	grants, err := m.routes.All()
	if err != nil {
		return heldRecords(sp)
	}
	reader := m.reader(sp)
	released, failed := 0, error(nil)
	for _, g := range grants {
		if g.Revoked {
			continue
		}
		mine := heldOfProject(held, g.ProjectIDHash)
		if len(mine) == 0 {
			continue
		}
		n, err := reader.ReleaseHeld(sessionread.ProjectOf(g), mine)
		released += n
		if err != nil {
			failed = err
			break
		}
	}
	if released > 0 {
		f.Fixedf("%d held segment(s) read cleanly under this build and will be uploaded", released)
	}
	if failed != nil {
		f.Problemf("a segment held on this machine could not be moved to where uploads read it: %v", failed)
		f.Detailf("It stays held; nothing was rewritten, and nothing was lost.")
	}
	return heldRecords(sp)
}

// heldOfProject is the held records one project's grant accounts for.
func heldOfProject(held []spool.Record, projectIDHash string) []spool.Record {
	var mine []spool.Record
	for _, r := range held {
		if r.ProjectIDHash != "" && r.ProjectIDHash == projectIDHash {
			mine = append(mine, r)
		}
	}
	return mine
}

// doctorEarlierSessions puts this project's session files back on the
// one path that reads them: what it already had when it was enabled
// is registered, and every entry still waiting for its first reading
// is asked for, through the calls enable makes. Nothing but enable
// ever looked for the files, and only a hook ever names a registered
// one, so an install made by a build that registered them without
// reading them, and a registration whose reading never ran, would
// otherwise keep them on disk unread for good.
//
// It repairs only what the user asked for. A project enabled with its
// earlier sessions skipped keeps that answer for the files it is
// about: the ones written before the grant are not searched for, not
// registered and not asked for, while an entry written after it is
// repaired as in any other project. The choice is about the age of a
// file, never about whether a reading that stopped is repaired. A
// project whose files this process cannot reach is left alone.
//
// What it registered leaves the diagnosis it repaired from, so the run
// reports the repair rather than the state it has just ended; what it
// cannot explain — a session written after the grant that no hook
// reported — is untouched and still reported.
//
// A repair it could not make is a problem of its own, never a silent
// run: a registration that failed to be written, and a reading no
// reader took, both leave the files where they were. Only an ask a
// reader took is reported as done.
func (m *Machine) doctorEarlierSessions(ctx context.Context, f *report.Findings, d *report.Diagnosis) {
	st := d.Project
	if !st.Enabled || st.WindowsSideClaude {
		return
	}
	registered, toRead, err := m.earlierSessionsRepair(d.SessionFiles, st)
	if err != nil {
		f.Problemf("this project's session files could not be put back on the path that reads them: %v", err)
		return
	}
	if len(toRead) == 0 {
		return
	}
	if err := m.readEarlierSessions(ctx, st, toRead); err != nil {
		f.Problemf("%d never-read session file(s) of this project could not be asked for: %v", len(toRead), err)
		f.Detailf("The next session of this project reads them, where a reader can be started for it.")
		return
	}
	if registered == 0 {
		f.Fixedf("asked for %d never-read session file(s) of this project to be read", len(toRead))
		return
	}
	f.Fixedf("registered %d session file(s) of this project that no hook reported, and asked for them to be read", registered)
	d.SessionFiles.Unregistered -= d.SessionFiles.Earlier
	d.SessionFiles.Earlier = 0
}

// earlierSessionsRepair makes the registrations this project is
// short of and reports what is left to read. The walk is paid for
// only where the diagnosis found session files the registry does not
// hold: registering is what that walk is for, and an entry already
// registered is found without it. It is not paid for at all in a
// project whose earlier files the user asked enable to leave alone —
// what such a walk would find is exactly what must not be
// registered.
//
// Of what the walk finds, only the sessions written before the grant
// are registered, judged by the same question that counted them. A
// session written after the grant that no hook reported is what this
// device cannot explain: registering it here would end the report of
// it, and one run of doctor would silence a broken hook for good.
func (m *Machine) earlierSessionsRepair(s report.SessionFilesState, st report.ProjectStatus) (registered int, toRead []string, err error) {
	if s.Earlier == 0 || st.EarlierSkipped {
		toRead, err = m.unreadSessions(st)
		return 0, toRead, err
	}
	found, err := discover.Walk(st.Root, m.claude().ConfigDir)
	if err != nil {
		return 0, nil, err
	}
	earlier := found.OnlySessions(func(session string) bool { return earlierThanGrant(session, st.GrantedAt) })
	return m.registerEarlierSessions(st, earlier, nil)
}

// doctorProxy checks who holds the proxy port. An unproven holder is
// the one finding doctor must shout about and can never repair:
// injected projects would send credentials to it. The verdict's reason
// decides the advice — only a proven stranger earns the stop-the-process
// remedy; an authentication failure may be the user's own proxy. Which
// of our own proxies doctor replaces and which it leaves alone is the
// verdict's own answer, never a version comparison made here. A proxy
// that is down is started only when wanted: a project the run covers
// is enabled.
func (m *Machine) doctorProxy(ctx context.Context, f *report.Findings, d report.Diagnosis, wanted bool) {
	h := d.Proxy.Health
	up := time.Duration(h.UptimeSeconds) * time.Second
	switch {
	case d.Proxy.Replaceable(m.deps.Version):
		if err := m.proxy.Ensure(ctx); err != nil {
			if cannot, ok := errors.AsType[*proxylife.CannotStartError](err); ok {
				f.ProxyCannotStart(cannot.Version, cannot.Cause)
				return
			}
			f.Problemf("a trajector proxy version %s holds %s and could not be replaced: %v", h.Version, d.Proxy.Addr, err)
			return
		}
		f.Fixedf("replaced the version %s proxy at %s with this build (%s)", h.Version, d.Proxy.Addr, m.deps.Version)
	case d.Proxy.Serving(m.deps.Version):
		if h.Version != m.deps.Version {
			f.OKf("proxy running at %s (version %s, up %s); %s", d.Proxy.Addr, h.Version, up, proxylife.ReuseReason)
			return
		}
		f.OKf("proxy running at %s (version %s, up %s)", d.Proxy.Addr, h.Version, up)
	case d.Proxy.Holder == proxylife.HolderForeign:
		f.ProxyProblem(d.Proxy.Reason, d.ProxyHolder)
	default:
		if d.ProxyCannotStart && (!wanted || d.ProxyIdleBetweenSessions) {
			// Nothing below starts the proxy for this run, so a build on
			// record as unable to is reported as it stands.
			f.ProxyCannotStart(m.deps.Version, nil)
			return
		}
		if !wanted {
			f.OKf("proxy not running; it starts on demand with the next session")
			return
		}
		if d.ProxyIdleBetweenSessions {
			// Nothing to start: the resident process comes up with the
			// next session, and a device where no project routes through
			// it has no traffic waiting on it in between.
			f.OKf("proxy not running; on this device it runs only while a session is open, because every enabled project records without it")
			return
		}
		if err := m.proxy.Ensure(ctx); err != nil {
			if cannot, ok := errors.AsType[*proxylife.CannotStartError](err); ok {
				f.ProxyCannotStart(cannot.Version, cannot.Cause)
				return
			}
			f.Problemf("the proxy is down and could not be started: %v", err)
			return
		}
		f.Fixedf("started the capture proxy at %s", d.Proxy.Addr)
	}
}

// doctorInjection reconciles the project's injected settings with the
// routing table. The enable invariant — an injected base URL implies an
// active token and both hooks — is restored where the settings file
// alone is wrong; disagreements about consent are reported, never
// guessed at.
func (m *Machine) doctorInjection(ctx context.Context, f *report.Findings, st report.ProjectStatus) error {
	settingsPath := st.SettingsPath()
	switch {
	case st.TableUnreadable != nil:
		// Enabled is unknown, not false: reconciling against it would
		// take a standing injection for a stale one and remove it. The
		// device finding already states the table.
		return nil //nolint:nilerr // the device finding reports the table; it is not this step's failure

	case !st.Enabled && !st.Injected:
		f.OKf("this project is not enabled; nothing to reconcile")
		return nil

	case st.Enabled && !st.Injected:
		// Re-injecting would resume capture the user may have stopped on
		// purpose by hand-editing; consent questions are theirs to answer.
		f.Problemf("the routing table grants this project but its settings inject nothing.")
		f.Detailf("Run `trajector enable` %s to restore the injection,", routing.ReEnableCaveat)
		f.Detailf("or `trajector disable` to withdraw this project.")
		return nil

	case !st.Enabled && st.Injected:
		orphaned, err := m.injectionOutlivedGrant(st)
		if err != nil {
			return err
		}
		if orphaned {
			// Removing it would leave the chain naming nothing, and no
			// grant says what the injection wrote over, so the user's own
			// value would be gone for good. It stays until they put that
			// value back.
			f.Problemf("%v; doctor leaves the injection in %s in place", ErrInjectionWithoutGrant, settingsPath)
			for _, step := range injectionWithoutGrantSteps() {
				f.Detailf("%s", step)
			}
			return nil
		}
		restored, unrestored, err := m.removeInjection(st.Root)
		if err != nil {
			f.Problemf("a stale injection points traffic at a token that no longer records, and removing it failed: %v", err)
			return nil
		}
		f.Fixedf("removed a stale injection from %s (its token no longer records)", settingsPath)
		if restored != "" {
			f.Detailf("Put back the base URL of your own that the injection had displaced: %s", restored)
		}
		if unrestored != "" {
			f.Problemf("%s", unrestoredBaseURLWarning(settingsPath, unrestored))
		}
		undone, failures := m.restoreRecordedSettings(st.Root, st.Hash, claudesettings.RequestedSettings)
		for _, key := range undone {
			f.Detailf("%s", settingRestoredLine(key))
		}
		for _, err := range failures {
			f.Problemf("%v", err)
		}
		return nil
	}

	if st.IdentityDisagreement() {
		f.Problemf("the routing table and the consent record disagree about this project's identity.")
		f.Detailf("Run `trajector disable` and then `trajector enable` %s to record both afresh.", routing.ReEnableCaveat)
		return nil
	}
	switch {
	case st.MissingSessionHooks():
		// The injection is the one the grant records and lacks only
		// hooks this release adds: they are added, the way an upgrade
		// adds them, and nothing else of the file is written.
		added, err := claudesettings.CompleteProjectHooks(settingsPath, projectHooks(m.deps.ExecPath))
		if err != nil {
			return fmt.Errorf("completing the session hooks in %s: %w", settingsPath, err)
		}
		f.Fixedf("completed the session hooks in %s (%d added)", settingsPath, added)
	case !st.Consistent():
		// The file is rewritten in the shape the grant records: the shape
		// was the user's choice at enable time, and the grant is where
		// enable wrote that choice down.
		restored, unrestored, err := m.injectProject(st, st.Token, st.Shape)
		if err != nil {
			return fmt.Errorf("repairing the injection in %s: %w", settingsPath, err)
		}
		f.Fixedf("rewrote the injection in %s (token and session hooks restored)", settingsPath)
		if restored != "" {
			f.Detailf("Put back the base URL of your own that the injection had displaced: %s", restored)
		}
		if unrestored != "" {
			f.Problemf("%s", unrestoredBaseURLWarning(settingsPath, unrestored))
		}
	default:
		f.OKf("injection and routing agree for this project")
	}

	if action, err := claudesettings.EnsureGitIgnored(ctx, st.Root, claudesettings.ProjectLocalIgnoreRule); err != nil {
		f.Problemf("could not verify .gitignore covers %s: %v", claudesettings.ProjectLocalIgnoreRule, err)
	} else if action == claudesettings.IgnoreAppended {
		f.Fixedf("added %s to .gitignore", claudesettings.ProjectLocalIgnoreRule)
	} else if action == claudesettings.IgnoreSymlinked {
		f.Problemf(".gitignore is a symbolic link and was left alone; add %s to your git ignores yourself", claudesettings.ProjectLocalIgnoreRule)
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
		f.Problemf("%s is set: Bedrock and Vertex channels are not supported, so this project's traffic is not being captured.", want.unsupportedKey)
	case refused:
		f.Problemf("this project's base-URL configuration moved to %s, which is refused: %s. The recorded upstream is unchanged.", want.upstream, nonLoopbackUpstreamRemedy)
	case err != nil:
		f.Problemf("this project's base URL moved to %s but the routing table could not be updated: %v", want.upstream, err)
	case moved:
		f.Fixedf("updated this project's upstream to %s (its base-URL configuration moved)", want.upstream)
	}
}

// doctorDiscoveryHint re-adds the user-level discovery hook a paired
// device is supposed to carry. Unpaired devices are left alone: login
// owns the first installation.
func (m *Machine) doctorDiscoveryHint(f *report.Findings, ts report.TokenStoreState) {
	if !ts.Paired {
		return
	}
	userSettings := m.claude().UserSettingsPath()
	if claudesettings.HasHook(userSettings, claudesettings.DiscoveryMarker) {
		return
	}
	if err := claudesettings.InjectUserHook(userSettings, hookCommand(m.deps.ExecPath, claudesettings.HookDiscovery)); err != nil {
		f.Problemf("the project-discovery hint is missing from %s and could not be re-added: %v", userSettings, err)
		return
	}
	f.Fixedf("re-added the project-discovery hint to %s", userSettings)
}

// doctorStaleDiscoveryHook takes out the discovery hook an earlier
// build of trajector wrote into the settings file of the default
// configuration directory, on a device that names another one. It is
// trajector's own hook and nothing else, so it goes out through the
// removal disable and uninstall use: the walk drops the entries
// carrying trajector's marker and leaves the rest of the file as the
// user wrote it.
func (m *Machine) doctorStaleDiscoveryHook(f *report.Findings, d report.Diagnosis) {
	if !d.StaleDiscoveryHook {
		return
	}
	path, moved := m.claude().UnreadUserSettingsPath()
	if !moved {
		return
	}
	report.DoctorStaleDiscoveryHook(f, claudesettings.RemoveUserHook(path))
}

// doctorSelfcheck closes an enabled project's diagnosis by asking the
// live proxy itself — the same self-check enable runs. Files can look
// right while the running proxy holds a state that will not record;
// only the proxy's own answer settles it. reportedPause is the pause
// the device part of the run reported.
func (m *Machine) doctorSelfcheck(f *report.Findings, d report.Diagnosis, reportedPause routing.PauseReason) {
	if !d.Project.Enabled || d.Proxy.Holder != proxylife.HolderOurs {
		return
	}
	if d.Selfcheck == nil {
		f.Problemf("the live proxy did not answer this project's self-check; run `trajector doctor` again once it settles")
		return
	}
	if err := m.explainSelfcheck(*d.Selfcheck); err != nil {
		if !d.Selfcheck.Recording && report.DoctorSelfcheckPaused(f, reportedPause, routing.PauseReason(d.Selfcheck.PauseReason)) {
			return
		}
		f.Problemf("the files agree, but the live proxy will not record this project: %v", err)
		return
	}
	f.OKf("live proxy confirms this project routes and records")
}
