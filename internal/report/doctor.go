package report

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// severity orders what a doctor run found. Only problems count toward
// the exit code: repairs already happened and notes carry no action.
type severity int

const (
	severityOK severity = iota
	severityFixed
	severityNote
	severityProblem
)

func (s severity) label() string {
	switch s {
	case severityFixed:
		return "fixed"
	case severityNote:
		return "note"
	case severityProblem:
		return "problem"
	default:
		return "ok"
	}
}

// finding is one fact a doctor run established, as a value: severity,
// the sentence, and any follow-up lines. The exit count derives from
// the severities, not from counting as text is printed.
type finding struct {
	severity severity
	text     string
	details  []string
}

// Findings accumulates what a doctor run establishes. The sections a
// Diagnosis alone answers are written by this package; the repairs are
// the machine's, and they write here too, so one run reads as one
// report whichever half produced a line.
type Findings struct {
	found []finding
}

func (f *Findings) add(sev severity, format string, a ...any) {
	f.found = append(f.found, finding{severity: sev, text: fmt.Sprintf(format, a...)})
}

// OK records a check that passed.
func (f *Findings) OK(format string, a ...any) { f.add(severityOK, format, a...) }

// Fixed records a repair this run already made.
func (f *Findings) Fixed(format string, a ...any) { f.add(severityFixed, format, a...) }

// Problem records something the user still has to act on.
func (f *Findings) Problem(format string, a ...any) { f.add(severityProblem, format, a...) }

// note records something the user should read that doctor can neither
// verify nor fix, so it never counts toward the exit code. Every note a
// run prints comes from a diagnosis rather than from a repair, which is
// why it is this package's to write and not the machine's.
func (f *Findings) note(format string, a ...any) { f.add(severityNote, format, a...) }

// Detail attaches a follow-up line to the most recent finding.
func (f *Findings) Detail(format string, a ...any) {
	last := &f.found[len(f.found)-1]
	last.details = append(last.details, fmt.Sprintf(format, a...))
}

// Problems counts the findings the user still must act on.
func (f *Findings) Problems() int {
	n := 0
	for _, found := range f.found {
		if found.severity == severityProblem {
			n++
		}
	}
	return n
}

// Render writes every finding in the order it was established.
func (f *Findings) Render(out io.Writer) {
	for _, found := range f.found {
		fmt.Fprintf(out, "  %s: %s\n", found.severity.label(), found.text)
		for _, d := range found.details {
			fmt.Fprintf(out, "      %s\n", d)
		}
	}
}

// DoctorDevice reports the two device-wide facts a doctor run opens
// with: whether the pairing state could be read at all, and whether
// recording is paused everywhere.
func DoctorDevice(f *Findings, d Diagnosis) {
	doctorTokenStore(f, d.TokenStore)
	doctorPause(f, d.Project)
}

// DoctorData reports the captured data on this machine: the spool that
// holds it, the quarantine it may be waiting in, every reason it is not
// moving, and anything else the service last said.
func DoctorData(f *Findings, d Diagnosis) {
	doctorSpool(f, d.Spool)
	doctorRejected(f, d)
	doctorStandings(f, d.Standings)
	doctorService(f, d.Handshake)
}

// doctorTokenStore distinguishes signed-out from cannot-read: an
// unreadable token store leaves the pairing state unknown, and unknown
// must never present as the signed-out state.
func doctorTokenStore(f *Findings, ts TokenStoreState) {
	if ts.Err == nil {
		return
	}
	f.Problem("the device token store could not be read: %v", ts.Err)
	f.Detail("Pairing state is unknown; this is not the signed-out state. If the OS")
	f.Detail("keyring is unavailable here, set %s=file and run `trajector login`.", tokenstore.BackendEnv)
}

// doctorPause reports a device-wide pause. Doctor never lifts one —
// signing in and reconfirming the agreement are the user's decisions —
// so this is always a problem with the lifting command named.
func doctorPause(f *Findings, st ProjectStatus) {
	if st.PauseReason == "" {
		return
	}
	f.Problem("recording is paused everywhere: %s", st.PauseReason.Explain())
}

// Discovery is what a doctor run found by looking for the current
// project's session files on disk and holding them against the
// registry. Only doctor looks: the search walks the project's
// directory tree, which status never pays for, and it registers
// nothing — registering is enable's and the session hooks' alone.
type Discovery struct {
	// Err, when non-nil, means the search could not run; every other
	// field is then zero.
	Err error
	// Unregistered counts the sessions whose files exist but which no
	// hook of trajector's ever reported: the one sign that the hooks
	// did not run for them.
	Unregistered int
	// Gaps is what this search could not cover.
	Gaps follow.Gaps
}

// DoctorProject reports what a diagnosis and a search of the current
// project's session files establish about it, and the next step where
// there is one: an arrangement across a WSL boundary that records
// nothing, the static reading of whether the hooks load, sessions the
// hooks never reported, and the directories the search could not
// cover. It says nothing about Remote Control: that is a choice made
// at enable time and shown by status, not a fault.
func DoctorProject(f *Findings, d Diagnosis, disc Discovery) {
	st := d.Project
	if !st.Enabled {
		return
	}
	if st.WindowsSideClaude {
		f.Problem("%s", windowsSideClaudeFact)
		f.Detail("%s", windowsSideWayOut)
	}
	doctorHookPolicy(f, d)
	doctorDiscovery(f, d, disc)
}

// doctorHookPolicy states the static reading of whether the hooks
// load and, when they will not, what to change. It is never a
// problem: the setting is the user's or their organization's to
// keep, and a doctor run on a device that keeps it must still pass.
func doctorHookPolicy(f *Findings, d Diagnosis) {
	p := d.HookPolicy
	if p == nil {
		return
	}
	if p.Runs {
		f.OK("%s", hooksWillLoad)
		return
	}
	f.note("%s (%s)", HooksWillNotLoad, p.Reason)
	f.Detail("Change that setting where it is set, or ask whoever manages it to.")
	if d.Project.NoProxy {
		f.Detail("%s. %s", nothingRecordedNow, noProxyWayOut)
	} else {
		f.Detail("%s.", ProxyHalfOnly)
	}
}

// doctorDiscovery holds the session files found on disk against the
// registry. Files no hook reported are the one observation that says
// the hooks did not run: when nothing readable explains why, the
// unrecorded cause left is the workspace-trust dialog, and that is
// the next step. Everything the search could not cover is stated as
// it is, never counted as a fault.
func doctorDiscovery(f *Findings, d Diagnosis, disc Discovery) {
	if err := d.SessionFiles.Err; err != nil {
		f.Problem("the session file registry could not be read: %v", err)
		return
	}
	if disc.Err != nil {
		f.Problem("could not look for this project's session files: %v", disc.Err)
		return
	}
	switch {
	case d.Project.WindowsSideClaude:
		// Nothing was looked for: the files are on the other side, and
		// the arrangement is already reported above with its way out.
	case disc.Unregistered == 0:
		f.OK("every session file of this project is registered (%d session(s))", d.SessionFiles.Sessions)
	case d.HookPolicy != nil && !d.HookPolicy.Runs:
		f.note("%d session(s) of this project were written without a hook of trajector's reporting them, which follows from the setting above", disc.Unregistered)
	default:
		f.Problem("%s", workspaceNotTrusted)
		f.Detail("%d session(s) of this project were written without a hook of trajector's reporting them, and nothing readable", disc.Unregistered)
		f.Detail("on this device keeps the hooks from loading. Claude Code runs them only once the workspace is trusted.")
	}
	for _, line := range gapLines(disc.Gaps) {
		f.note("%s", line)
	}
}

// doctorSpool verifies the capture spool accepts writes within quota.
func doctorSpool(f *Findings, s SpoolState) {
	if s.OpenErr != nil {
		f.Problem("%s", spoolUnusableHeadline(s))
		return
	}
	if s.WritableErr != nil {
		f.Problem("%s", spoolUnwritableHeadline(s.WritableErr))
		f.Detail("Spool: %s of %s used at %s.", platform.HumanBytes(s.Usage), platform.HumanBytes(s.Quota), s.Dir)
		if s.full() {
			f.Detail("%s", spoolFullRemedy)
		}
		return
	}
	f.OK("capture spool writable (%s of %s used)", platform.HumanBytes(s.Usage), platform.HumanBytes(s.Quota))
}

// doctorRejected surfaces quarantined batches. They are never deleted
// or retried automatically — what happens to them is the user's call —
// so doctor lists each with its recorded reason and the commands that
// end its wait. The two causes part ways on the remedy: a batch the
// service refused can be requeued, while records this machine set aside
// as unreadable can never re-enter the spool, so offering requeue for
// them would send the user to a command that refuses.
func doctorRejected(f *Findings, d Diagnosis) {
	if d.RejectedErr != nil {
		f.Problem("%s", rejectedUnreadableHeadline(d))
		return
	}
	if len(d.Rejected) == 0 {
		f.OK("no rejected batches quarantined")
		return
	}
	f.Problem("%s:", quarantineHeadline(d.Rejected))
	refused, unreadable := false, false
	for _, b := range d.Rejected {
		line := fmt.Sprintf("%s: %d record(s)", b.BatchID, b.Records)
		when := ""
		if !b.Reason.At.IsZero() {
			when = " " + b.Reason.At.UTC().Format(time.RFC3339)
		}
		if b.Reason.Cause == upload.CauseUnreadable {
			unreadable = true
			line += ", set aside" + when + " — never sent: unreadable in the spool"
		} else {
			refused = true
			if when != "" {
				line += ", rejected" + when
			}
		}
		if b.Reason.Details != "" {
			line += " (" + b.Reason.Details + ")"
		}
		f.Detail("%s", line)
	}
	if refused {
		f.Detail("Run `trajector doctor requeue <batch-id>` (or `--all`) to upload them again,")
		f.Detail("or `trajector doctor discard <batch-id>` (or `--all`) to delete them for good.")
	}
	if unreadable {
		f.Detail("Unreadable records cannot be requeued; `trajector doctor discard <batch-id>` deletes them for good.")
	}
}

// doctorStandings explains every reason uploads are held back, each in
// the same shape: the standing's own sentence, then the service's words
// if it supplied any, then the standing's own remedy. Reading three
// gates that stopped uploads for three different reasons therefore
// takes learning one shape, and none of the wording is doctor's to
// choose.
//
// None of them is a problem. Being behind the service's minimum, or
// waiting out a pause, or holding an account whose data authorization
// is unfinished, is not a broken machine: nothing doctor can do would
// change any of it, and counting them would fail `trajector doctor` on
// a healthy install over states the user resolves elsewhere.
func doctorStandings(f *Findings, standings []upload.Standing) {
	for _, s := range standings {
		f.note("%s", doctorClause(s.Explain()))
		if s.Message != "" {
			f.Detail(ServiceSays, s.Message)
		}
		if remedy := s.Remedy(); remedy != "" {
			f.Detail("%s", remedy)
		}
		if s.Reason == upload.AuthorizationGate {
			// The one gate a user is likeliest to read as data loss: the
			// service refused the account, not the batch.
			f.Detail("Captured data is kept; nothing was quarantined.")
		}
	}
}

// doctorService relays what the service last said about anything other
// than uploading, which is the notice and nothing else — every reason
// it gave for not uploading is a standing.
func doctorService(f *Findings, h platform.Handshake) {
	if h.Notice != "" {
		f.note("notice from the service: %s", h.Notice)
	}
}

// quarantineHeadline is the one sentence both status and doctor use for
// quarantined batches, so the two surfaces cannot drift apart.
func quarantineHeadline(rejected []upload.RejectedBatch) string {
	records := 0
	for _, b := range rejected {
		records += b.Records
	}
	return fmt.Sprintf("%d record(s) in %d rejected batch(es) are quarantined and will not be retried automatically", records, len(rejected))
}

// spoolUnusableHeadline is the one sentence both status and doctor use
// for a spool that could not be opened or read, so the two surfaces
// cannot drift apart.
func spoolUnusableHeadline(s SpoolState) string {
	return fmt.Sprintf("the capture spool at %s is not usable: %v", s.Dir, s.OpenErr)
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
func rejectedUnreadableHeadline(d Diagnosis) string {
	return fmt.Sprintf("the rejected batches at %s could not be read: %v", d.RejectedDir, d.RejectedErr)
}

// DoctorEnvironment points out platform topologies that need care.
// These are informational: there is no reliable signal to check against,
// so nothing here counts as a problem.
func DoctorEnvironment(f *Findings) {
	if runtime.GOOS == "linux" && isWSL() {
		f.OK("running under WSL: Claude Code must also run inside WSL; a Windows-native claude cannot reach this trajector")
	}
	if runtime.GOOS == "windows" {
		f.OK("Windows: if a firewall prompt appears for the proxy, allow loopback access; the proxy only ever binds 127.0.0.1")
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
