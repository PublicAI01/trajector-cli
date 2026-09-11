package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// Dashboard prints the device dashboard: pairing, the current project's
// consent, the proxy, the spool, uploads, and what the service last
// said. It renders a Diagnosis and nothing else — it never repairs
// anything, always leaves the fixing to doctor, and never starts a
// proxy just to look at one. A store that could not be read is that
// section's warning, never a reason to cut the sections after it.
func Dashboard(w io.Writer, d Diagnosis) {
	fmt.Fprintf(w, "trajector %s\n", d.Version)
	st := d.Project

	fmt.Fprintln(w, "\nDevice")
	switch {
	case d.TokenStore.Err != nil:
		fmt.Fprintln(w, "  WARNING: the device token store could not be read. Run `trajector doctor`.")
	case d.TokenStore.Paired:
		fmt.Fprintln(w, "  Signed in.")
	default:
		fmt.Fprintln(w, "  Not signed in. Run `trajector login` to pair this device.")
	}
	if st.PauseReason != "" {
		fmt.Fprintf(w, "  Recording is paused everywhere: %s.\n", st.PauseReason.Explain())
	}

	fmt.Fprintf(w, "\nProject %s\n", st.Root)
	switch {
	case st.InjectionAgrees:
		if st.PauseReason != "" {
			fmt.Fprintln(w, "  Contributing; recording is paused for now (see Device above).")
		} else {
			fmt.Fprintln(w, "  Contributing; recording is on for this project.")
		}
		for _, line := range projectLines(d) {
			fmt.Fprintf(w, "  %s\n", line)
		}
		for _, line := range optionalSettingLines(d.OptionalSettings) {
			fmt.Fprintf(w, "  %s\n", line)
		}
	case !st.Enabled && !st.Injected:
		fmt.Fprintln(w, "  Not enabled. Run `trajector enable` to contribute from this project.")
	default:
		fmt.Fprintln(w, "  WARNING: the injected settings and the routing table disagree. Run `trajector doctor`.")
	}

	fmt.Fprintln(w, "\nProxy")
	switch d.Proxy.Holder {
	case proxylife.HolderOurs:
		h := d.Proxy.Health
		up := time.Duration(h.UptimeSeconds) * time.Second
		fmt.Fprintf(w, "  Running at %s: version %s, up %s.\n", d.Proxy.Addr, h.Version, up)
		// These counters live in the running proxy's memory, so they
		// begin at the uptime printed on the line above — not at
		// midnight. The proxy restarts often enough (idle exit, version
		// handover, reboot) that calling them a day's work made the
		// number read low, in the one direction a user reads as "it is
		// not recording". They are named for what they actually count.
		fmt.Fprintf(w, "  Recorded since it started: %d (SSE degraded: %d, dropped: %d).\n",
			h.RecordedToday, h.SSEDegradedToday, h.CapturesDropped)
		if n := len(h.RecentRecordingErrors); n > 0 {
			fmt.Fprintf(w, "  Recent recording errors: %d (last: %s)\n", n, h.RecentRecordingErrors[n-1])
		}
	case proxylife.HolderForeign:
		fmt.Fprintf(w, "  WARNING: %v.\n", d.Proxy.Reason)
		if remedy := ProxyRemedy(d.Proxy.Reason); remedy != "" {
			fmt.Fprintf(w, "  %s\n", remedy)
		}
	default:
		if d.ProxyIdleBetweenSessions {
			fmt.Fprintln(w, "  Not running; on this device it runs only while a session is open, because every enabled project records without it.")
		} else {
			fmt.Fprintln(w, "  Not running; it starts on demand with the next session.")
		}
	}

	fmt.Fprintln(w, "\nSpool")
	switch {
	case d.Spool.OpenErr != nil:
		fmt.Fprintf(w, "  WARNING: %s.\n", spoolUnusableHeadline(d.Spool))
		fmt.Fprintln(w, "  Run `trajector doctor`.")
	case d.Spool.WritableErr != nil:
		fmt.Fprintf(w, "  %s of %s used.\n", platform.HumanBytes(d.Spool.Usage), platform.HumanBytes(d.Spool.Quota))
		fmt.Fprintf(w, "  WARNING: %s.\n", spoolUnwritableHeadline(d.Spool.WritableErr))
		if d.Spool.full() {
			fmt.Fprintf(w, "  %s\n", spoolFullRemedy)
		} else {
			fmt.Fprintln(w, "  Run `trajector doctor`.")
		}
	default:
		fmt.Fprintf(w, "  %s of %s used.\n", platform.HumanBytes(d.Spool.Usage), platform.HumanBytes(d.Spool.Quota))
	}

	fmt.Fprintln(w, "\nUploads")
	if r := d.Uploads.LastUpload; r != nil {
		fmt.Fprintf(w, "  Last upload: %d record(s) (%s) at %s.\n",
			r.Records, platform.HumanBytes(r.Bytes), r.At.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintln(w, "  Never uploaded.")
	}
	if d.Uploads.LastError != "" {
		fmt.Fprintf(w, "  Last error: %s (%s).\n", d.Uploads.LastError, d.Uploads.LastErrorAt.UTC().Format(time.RFC3339))
	}
	if d.Spool.OpenErr == nil {
		fmt.Fprintf(w, "  %s\n", recordsWaitingLine(d.Spool))
	}
	switch {
	case d.RejectedErr != nil:
		fmt.Fprintf(w, "  WARNING: %s.\n", rejectedUnreadableHeadline(d))
		fmt.Fprintln(w, "  Run `trajector doctor`.")
	case len(d.Rejected) > 0:
		fmt.Fprintf(w, "  WARNING: %s.\n", quarantineHeadline(d.Rejected))
		fmt.Fprintln(w, "  Run `trajector doctor` to inspect them, then requeue or discard them.")
	}

	// Every reason uploads are held back is printed here, in the order
	// the uploader itself meets them, each from its own two sentences.
	// The service's own words come between them: they may say why, or by
	// when, and a user who reads only one more line should read the
	// reason rather than the remedy.
	for _, s := range d.Standings {
		fmt.Fprintf(w, "  %s\n", s.Explain())
		if s.Message != "" {
			fmt.Fprintf(w, "  %s\n", ServiceWords(s.Message))
		}
		if remedy := s.Remedy(); remedy != "" {
			fmt.Fprintf(w, "  %s\n", remedy)
		}
	}

	if d.Handshake.Notice != "" {
		fmt.Fprintln(w, "\nService")
		fmt.Fprintf(w, "  Notice from the service: %s\n", d.Handshake.Notice)
	}
}

// projectLines follows the contributing line with everything else
// status states about an enabled project, one fact per line: the
// shape it records in and what that shape costs, the static reading
// of whether its hooks load, a hook doctor still has to add, and what
// the registry says about its session files.
func projectLines(d Diagnosis) []string {
	st := d.Project
	var lines []string
	if st.WindowsSideClaude {
		lines = append(lines, "WARNING: "+windowsSideClaudeFact+". Run `trajector doctor`.")
	}
	if st.Shape != routing.WithoutProxy {
		// The upstream is where the proxy forwards to; a project whose
		// traffic never reaches the proxy has none to speak of.
		if st.Upstream != capture.Anthropic.OfficialUpstream {
			lines = append(lines, fmt.Sprintf("Upstream: %s (third-party origin).", st.Upstream))
		}
		if st.UpstreamMoved.Happened() {
			lines = append(lines, fmt.Sprintf("The upstream moved from %s at %s (base-URL configuration change).", st.UpstreamMoved.From, st.UpstreamMoved.At))
		}
	}
	lines = append(lines, ShapeNotice(st.Shape))
	lines = append(lines, hookJudgementLines(d)...)
	if st.MissingSessionEnd() {
		lines = append(lines, sessionEndMissingLine(st.SettingsPath()))
	}
	lines = append(lines, sessionFileLines(d.SessionFiles)...)
	return append(lines, signalLines(d)...)
}

// signalLines states what reading the project's session files noticed
// about their shape, as facts about the lines: while recording is
// paused for a shape this build's redaction does not cover, which
// fields that were; always, how many lines lacked what a record is
// later read by. Each line is a count or a field name, never a value
// from a session file, and none names a next step: the pause carries
// its own, and the counts have none a user could take.
func signalLines(d Diagnosis) []string {
	s := d.SessionFiles.Signals
	var lines []string
	if d.Project.PauseReason == routing.PauseRedactionDrift {
		if len(s.UnanchoredPathFields) > 0 {
			lines = append(lines, fmt.Sprintf("Fields in this project's session files holding a path that this build's redaction does not cover: %s.",
				strings.Join(s.UnanchoredPathFields, ", ")))
		}
		if s.IncompleteSegments > 0 {
			lines = append(lines, fmt.Sprintf("%d read(s) of this project's session files ended in a line without a newline.", s.IncompleteSegments))
		}
	}
	if s.AssistantLinesWithoutMessageID > 0 {
		lines = append(lines, fmt.Sprintf("%d of %d assistant lines in this project's session files carried no message id.",
			s.AssistantLinesWithoutMessageID, s.AssistantLines))
	}
	if s.AssistantLinesMissingResponseFields > 0 {
		lines = append(lines, fmt.Sprintf("%d of %d assistant lines in this project's session files lacked a response field a record is read by.",
			s.AssistantLinesMissingResponseFields, s.AssistantLines))
	}
	if s.MessagesWithBlockIndexGap > 0 {
		lines = append(lines, fmt.Sprintf("%d message(s) in this project's session files had block indexes that repeat or skip a number.",
			s.MessagesWithBlockIndexGap))
	}
	if s.AgentLinesWithoutParent > 0 {
		lines = append(lines, fmt.Sprintf("%d of %d agent lines in this project's session files named nothing they were started by.",
			s.AgentLinesWithoutParent, s.AgentLines))
	}
	return lines
}

// hookJudgementLines states the static reading of whether the hooks
// load and what follows from it for this project's shape. A project
// with no hooks to read about is silent about them.
func hookJudgementLines(d Diagnosis) []string {
	if d.HookPolicy == nil {
		return nil
	}
	return ExplainHooks(*d.HookPolicy, d.Project.Shape).Lines()
}

// sessionEndMissingLine names the one hook an injection made before
// that hook existed lacks, and the command that adds it.
func sessionEndMissingLine(settingsPath string) string {
	return fmt.Sprintf("The session-end hook is missing from %s; run `trajector doctor` to add it.", settingsPath)
}

// sessionFileLines is the registry's account of the project's session
// files: how many sessions, when one was last read, how much is not
// read yet, then what the search for earlier files could not cover,
// and the standing disclosure of what it cannot find by design. No
// line names a session or a session file.
func sessionFileLines(s SessionFilesState) []string {
	if s.Err != nil {
		return []string{fmt.Sprintf("WARNING: the session file registry could not be read: %v. Run `trajector doctor`.", s.Err)}
	}
	var lines []string
	if s.Sessions == 0 {
		lines = append(lines, "Session files: none registered yet.")
	} else {
		lastRead := "never"
		if !s.LastReadAt.IsZero() {
			lastRead = s.LastReadAt.UTC().Format(time.RFC3339)
		}
		lines = append(lines, fmt.Sprintf("Session files: %d session(s) registered; last read %s; %s not read yet.",
			s.Sessions, lastRead, platform.HumanBytes(s.BytesBehind)))
	}
	lines = append(lines, gapLines(s.Gaps)...)
	return append(lines, spellingVariantsNotice)
}

// gapLines states what the search for a project's earlier session
// files could not cover, one directory per line with its reason.
func gapLines(g follow.Gaps) []string {
	var lines []string
	if g.Truncated {
		lines = append(lines, treeLimitExceeded())
	}
	for _, a := range g.Ambiguous {
		lines = append(lines, ambiguityLine(a))
	}
	for _, dir := range g.Unreadable {
		lines = append(lines, fmt.Sprintf("Not looked at: %s could not be listed, nor anything below it.", dir))
	}
	return lines
}

// ambiguityLine says which directory is not collected and why: the
// other real directories Claude Code stores under the same name.
func ambiguityLine(a follow.Ambiguity) string {
	others := make([]string, 0, len(a.Matches))
	for _, m := range a.Matches {
		if m != a.Dir {
			others = append(others, m)
		}
	}
	return fmt.Sprintf("Not collected: %s stores its session files under the same name as %s, and they cannot be told apart.",
		a.Dir, strings.Join(others, ", "))
}

// recordsWaitingLine is how far behind uploading the records are: how
// many wait in the spool and of which kinds, and how old the oldest is.
// Every kind the spool holds is counted, so a device that records only
// through the proxy is never told it has nothing waiting. A kind with
// nothing waiting is left out rather than printed as a zero. On a
// device where the resident process lives only while a session is
// open, the wait ends with the next session, not on a schedule.
func recordsWaitingLine(s SpoolState) string {
	rawcalls, segments, snapshots := s.recordsWaiting()
	if rawcalls+segments+snapshots == 0 {
		return "Records waiting to upload: none."
	}
	var kinds []string
	count := func(n int, noun string) {
		if n > 0 {
			kinds = append(kinds, fmt.Sprintf("%d %s", n, noun))
		}
	}
	count(rawcalls, "rawcall(s)")
	count(segments, "segment(s)")
	count(snapshots, "snapshot(s)")
	line := "Records waiting to upload: " + strings.Join(kinds, ", ")
	if !s.OldestRecord.IsZero() {
		line += fmt.Sprintf("; the oldest is from %s", s.OldestRecord.UTC().Format(time.RFC3339))
	}
	return line + "."
}

// optionalSettingLines closes the Project section with the optional
// settings. A setting that is on is stated as on; a declined one keeps
// its single factual line and is never argued with again; only a
// setting that is off and was never declined gets the recommendation.
func optionalSettingLines(settings []OptionalSettingStatus) []string {
	var lines []string
	declined := 0
	var off []string
	for _, s := range settings {
		switch {
		case s.State == claudesettings.OnByUs:
			lines = append(lines, fmt.Sprintf("Optional settings: %s on (set by trajector).", s.Key))
		case s.State == claudesettings.OnByUser:
			lines = append(lines, fmt.Sprintf("Optional settings: %s on.", s.Key))
		case s.Declined:
			declined++
		default:
			off = append(off, s.Key)
		}
	}
	if declined > 0 {
		lines = append(lines, fmt.Sprintf("Optional settings: %d declined. Run `trajector enable` to review.", declined))
	}
	for _, key := range off {
		lines = append(lines, fmt.Sprintf("One optional setting is off: %s. "+
			"Turning it on costs you nothing and makes your records more complete. "+
			"Run `trajector enable` to see what it changes.", key))
	}
	return lines
}
