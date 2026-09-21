package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// Dashboard prints the device dashboard: the one-line verdict, then
// pairing, the current project's consent, the proxy, the spool,
// uploads, and what the service last said. It renders a Diagnosis and
// nothing else — it never repairs anything, always leaves the fixing to
// doctor, and never starts a proxy just to look at one. A store that
// could not be read is that section's own line, never a reason to cut
// the sections after it. It returns how many lines said something
// stopped working, which is what the command exits on.
func Dashboard(w io.Writer, style Style, d Diagnosis) int {
	fmt.Fprintln(w, Verdict(Recording(d), d.EnabledProjects))
	fmt.Fprintf(w, "trajector %s\n", d.Version)

	sections := []*section{
		deviceSection(d),
		projectSection(d),
		proxySection(d),
		spoolSection(d),
		uploadsSection(d),
	}
	if d.Handshake.Notice != "" {
		service := &section{name: "Service"}
		service.linef("Notice from the service: %s", d.Handshake.Notice)
		sections = append(sections, service)
	}
	errors := 0
	for _, s := range sections {
		s.render(w, style)
		errors += s.errors()
	}
	return errors
}

// deviceSection states what holds for every project on this machine:
// whether the device is paired, whether a pause stops all of them, and
// a hook of trajector's that is left where nothing reads it.
func deviceSection(d Diagnosis) *section {
	s := &section{name: "Device"}
	switch {
	case d.TokenStore.Err != nil:
		s.problem(tokenStoreUnreadableHeadline, tokenStoreUnreadableWhy(d.TokenStore.Err), tokenStoreUnreadableFix)
	case d.TokenStore.Paired:
		s.linef("Signed in.")
	default:
		s.linef("Not signed in. Run `trajector login` to pair this device.")
	}
	if d.Project.PauseReason != "" {
		why, fix := pauseWhyFixFor(d.Project)
		s.problem(pausedEverywhere, why, fix)
		for _, next := range pauseNextSteps(d.Project.PauseReason) {
			s.detail("%s", next)
		}
	}
	if d.StaleDiscoveryHook {
		s.warnFix(staleDiscoveryHookFact, staleDiscoveryHookWhy, fixDoctor)
	}
	return s
}

// projectSection states what holds for the project status was run in.
func projectSection(d Diagnosis) *section {
	st := d.Project
	s := &section{name: "Project " + st.Root}
	switch {
	case st.InjectionAgrees:
		if st.PauseReason != "" {
			s.linef("Contributing; recording is paused for now (see Device above).")
		} else {
			s.linef("Contributing; recording is on for this project.")
		}
		if st.WindowsSideClaude {
			s.warnFix(windowsSideClaudeFact, windowsSideClaudeWhy, fixDoctor)
			s.detail("%s", windowsSideWayOut)
		}
		if st.MissingSessionHooks() {
			s.problem(sessionHooksMissingHeadline, sessionHooksMissingWhy(st.SettingsPath()), fixDoctor)
		}
		for _, line := range projectLines(d) {
			s.linef("%s", line)
		}
		if d.SessionFiles.Err != nil {
			s.warnf("the session file registry could not be read: %v. Run `trajector doctor`.", d.SessionFiles.Err)
		}
		for _, line := range optionalSettingLines(d.OptionalSettings) {
			s.linef("%s", line)
		}
	case !st.Enabled && !st.Injected:
		s.linef("Not enabled. Run `trajector enable` to contribute from this project.")
	default:
		s.warnf("the injected settings and the routing table disagree. Run `trajector doctor`.")
	}
	return s
}

// proxySection states who holds the proxy port and what the running
// proxy has recorded since it started.
func proxySection(d Diagnosis) *section {
	s := &section{name: "Proxy"}
	switch d.Proxy.Holder {
	case proxylife.HolderOurs:
		h := d.Proxy.Health
		up := time.Duration(h.UptimeSeconds) * time.Second
		s.linef("Running at %s: version %s, up %s.", d.Proxy.Addr, h.Version, up)
		// These counters live in the running proxy's memory, so they
		// begin at the uptime printed on the line above — not at
		// midnight. The proxy restarts often enough (idle exit, version
		// handover, reboot) that calling them a day's work made the
		// number read low, in the one direction a user reads as "it is
		// not recording". They are named for what they actually count.
		s.linef("Recorded since it started: %d (SSE degraded: %d, dropped: %d).",
			h.RecordedToday, h.SSEDegradedToday, h.CapturesDropped)
		if n := len(h.RecentRecordingErrors); n > 0 {
			s.warnf("Recent recording errors: %d (last: %s)", n, h.RecentRecordingErrors[n-1])
		}
	case proxylife.HolderForeign:
		s.take(proxyProblem(d.Proxy.Reason))
	default:
		if d.ProxyIdleBetweenSessions {
			s.linef("Not running; on this device it runs only while a session is open, because every enabled project records without it.")
		} else {
			s.linef("Not running; it starts on demand with the next session.")
		}
	}
	return s
}

// spoolSection states how much of the spool is used and every reason it
// would refuse a write.
func spoolSection(d Diagnosis) *section {
	s := &section{name: "Spool"}
	switch {
	case d.Spool.OpenErr != nil:
		s.problem(trimPeriod(spoolUnusableHeadline(d.Spool)), "nothing can be recorded while the spool cannot be read", fixDoctor)
	case d.Spool.WritableErr != nil:
		s.linef("%s of %s used.", platform.HumanBytes(d.Spool.Usage), platform.HumanBytes(d.Spool.Quota))
		if d.Spool.full() {
			s.problem(spoolFullHeadline, spoolFullWhy, spoolFullFix)
		} else {
			s.problem(trimPeriod(spoolUnwritableHeadline(d.Spool.WritableErr)), "the spool refused a write, so nothing is recorded", fixDoctor)
		}
	default:
		s.linef("%s of %s used.", platform.HumanBytes(d.Spool.Usage), platform.HumanBytes(d.Spool.Quota))
	}
	if d.Spool.OpenErr == nil && d.Spool.Held.Records > 0 {
		s.linef("%s.", heldHeadline(d.Spool.Held))
	}
	return s
}

// uploadsSection states what left this machine, what waits, and every
// reason nothing is leaving right now.
func uploadsSection(d Diagnosis) *section {
	s := &section{name: "Uploads"}
	if r := d.Uploads.LastUpload; r != nil {
		s.linef("Last upload: %d record(s) (%s) at %s.",
			r.Records, platform.HumanBytes(r.Bytes), r.At.UTC().Format(time.RFC3339))
	} else {
		s.linef("Never uploaded.")
	}
	if d.Uploads.LastError != "" {
		s.linef("Last error: %s (%s).", d.Uploads.LastError, d.Uploads.LastErrorAt.UTC().Format(time.RFC3339))
	}
	if d.Spool.OpenErr == nil {
		s.linef("%s", recordsWaitingLine(d.Spool))
	}
	switch {
	case d.RejectedErr != nil:
		s.problem(trimPeriod(rejectedUnreadableHeadline(d)), "quarantined batches cannot be listed, so what waits there is unknown", fixDoctor)
	case len(d.Rejected) > 0:
		s.problem(quarantineHeadline(d.Rejected), quarantineWhy, quarantineFix)
		s.detail("%s", quarantineAllNote)
	}

	// Every reason uploads are held back is printed here, in the order
	// the uploader itself meets them, each from its own two sentences.
	// The service's own words come between them: they may say why, or by
	// when, and a user who reads only one more line should read the
	// reason rather than the remedy.
	for _, st := range d.Standings {
		s.linef("%s", st.Explain())
		if st.Message != "" {
			s.linef("%s", ServiceWords(st.Message))
		}
		if remedy := st.Remedy(); remedy != "" {
			s.linef("%s", remedy)
		}
	}
	return s
}

// projectLines follows the contributing line with everything else
// status states about an enabled project, one fact per line: the
// shape it records in and what that shape costs, what a call the proxy
// did not witness is rewarded at, the static reading of whether its
// hooks load, a hook doctor still has to add, and what the registry
// says about its session files.
func projectLines(d Diagnosis) []string {
	st := d.Project
	var lines []string
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
	lines = append(lines, UnwitnessedReward)
	lines = append(lines, hookJudgementLines(d)...)
	lines = append(lines, sessionFileLines(d.SessionFiles)...)
	if st.EarlierSkipped {
		lines = append(lines, earlierSessionsSkippedWayBack)
	}
	return append(lines, signalLines(d)...)
}

// signalLines states what reading the project's session files noticed
// about their shape, as facts about the lines: while recording is
// paused for a shape this build's redaction does not cover, which
// fields that were; always, how many lines lacked what a record is
// later read by. Each line is a count or a field name, never a value
// from a session file. Only one count names a next step, and only
// while a setting readable here would change it; for the others the
// pause carries its own and the user has none to take.
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
	if s.AssistantLinesWithEmptyReasoning > 0 {
		lines = append(lines, fmt.Sprintf(emptyReasoningFact, s.AssistantLinesWithEmptyReasoning, s.AssistantLines))
		if settingOff(d.OptionalSettings, claudesettings.KeyShowThinkingSummaries) {
			lines = append(lines, emptyReasoningWayOut)
		}
	}
	return lines
}

// settingOff reports that an optional Claude Code setting is off for
// this project: either nobody set it, or the user set it to false. A
// setting the diagnosis says nothing about is not reported off — a
// sentence that sends the user to a setting reads its state, and never
// guesses it from the absence of one.
func settingOff(settings []OptionalSettingStatus, key string) bool {
	for _, s := range settings {
		if s.Key == key {
			return s.State == claudesettings.Unset || s.State == claudesettings.OffByUser
		}
	}
	return false
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

// An injection made before a hook existed lacks it, which is stated in
// the three parts every problem with one command is stated in. The path
// belongs in the why: it says which file is short a hook, and the fix
// line carries the command alone.
const sessionHooksMissingHeadline = "a session hook is missing from this project's injected settings"

func sessionHooksMissingWhy(settingsPath string) string {
	return fmt.Sprintf("%s was injected before this hook existed, so part of this project is not reported", settingsPath)
}

// sessionFileLines is the registry's account of the project's session
// files: how many sessions, when one was last read, how much is not
// read yet, then what the search for earlier files could not cover,
// and the standing disclosure of what it cannot find by design. No
// line names a session or a session file.
func sessionFileLines(s SessionFilesState) []string {
	if s.Err != nil {
		return nil
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

// recordNouns is the user's word for each kind of record waiting. The
// word is the user's and not the wire's, so it lives here rather than
// beside the kind; a kind with no word here is not printed, because a
// count under no noun says nothing.
var recordNouns = map[envelope.Kind]string{
	envelope.KindRawcall:      "rawcall(s)",
	envelope.KindSegment:      "segment(s)",
	envelope.KindMetaSnapshot: "session snapshot(s)",
	envelope.KindGitSnapshot:  "git snapshot(s)",
}

// recordsWaitingLine is how far behind uploading the records are: how
// many wait in the spool and of which kinds, and how old the oldest is.
// Every kind the spool holds is counted, so a device that records only
// through the proxy is never told it has nothing waiting. A kind with
// nothing waiting is left out rather than printed as a zero. On a
// device where the resident process lives only while a session is
// open, the wait ends with the next session, not on a schedule.
func recordsWaitingLine(s SpoolState) string {
	waiting := s.recordsWaiting()
	if waiting.Total() == 0 {
		return "Records waiting to upload: none."
	}
	var kinds []string
	for _, kind := range envelope.Kinds() {
		if n := waiting[kind]; n > 0 && recordNouns[kind] != "" {
			kinds = append(kinds, fmt.Sprintf("%d %s", n, recordNouns[kind]))
		}
	}
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
