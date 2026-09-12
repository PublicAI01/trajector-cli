package report_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// doctorProjectText is what doctor prints about the current project
// from a diagnosis.
func doctorProjectText(d report.Diagnosis) (int, string) {
	f := &report.Findings{}
	report.DoctorProject(f, d)
	var b bytes.Buffer
	f.Render(&b)
	return f.Problems(), b.String()
}

// walked is a diagnosis whose session files were read from the
// project's tree, with the sessions that reading found and the
// registry does not hold.
func walked(d report.Diagnosis, unregistered int) report.Diagnosis {
	d.SessionFiles.Walked = true
	d.SessionFiles.Unregistered = unregistered
	return d
}

// enabledDevice is a paired device whose current project contributes
// in the shape with a base URL, with its hooks judged to load and one
// session registered.
func enabledDevice() report.Diagnosis {
	d := device()
	d.Project = contributing()
	d.HookPolicy = &claudesettings.HookPolicy{Runs: true}
	d.SessionFiles = report.SessionFilesState{Sessions: 1}
	return d
}

// withoutProxy turns an enabled device's project into the shape
// without a base URL.
func withoutProxy(d report.Diagnosis) report.Diagnosis {
	d.Project.InjectedBaseURL, d.Project.InjectedToken = "", ""
	d.Project.Shape = routing.WithoutProxy
	return d
}

const (
	hooksWillLoadLine    = "Judged from configuration readable on this machine, Claude Code will load trajector's hooks in this project"
	hooksWillNotLoadLine = "Judged from configuration readable on this machine, Claude Code will not load trajector's hooks in this project"
	proxyHalfOnlyLine    = "Only the proxy records this project for now; its session files are not read"
	nothingRecordedLine  = "Nothing is recorded from this project for now"
	remoteControlLine    = "Remote Control: inside this project, /remote-control will not be available. To use it, either start sessions with claude remote-control (both sources are still recorded), or run trajector enable --no-proxy to record only the session files (Remote Control stays available; records from one source may be rewarded differently)."
	noProxyFactLine      = "This project records from its session files only, so Remote Control stays available."
	treeLimitLine        = "This project's directory tree has more than 50000 directories, so the count above is incomplete."
	workspaceTrustLine   = "This workspace is not trusted yet; accept the trust dialog in Claude Code."
	spellingLine         = "Sessions started under another spelling of this project's path"
)

func TestStatusStatesEverySessionFileFact(t *testing.T) {
	ambiguity := follow.Ambiguity{
		Dir:     "/home/dev/sample-project/a-b",
		Name:    "-home-dev-sample-project-a-b",
		Matches: []string{"/home/dev/sample-project/a-b", "/home/dev/sample-project/a_b"},
	}
	for _, tc := range []struct {
		name   string
		state  report.SessionFilesState
		want   []string
		reject []string
	}{
		{
			name:  "none registered yet",
			state: report.SessionFilesState{},
			want:  []string{"Session files: none registered yet.", spellingLine},
		},
		{
			name:  "registered but never read",
			state: report.SessionFilesState{Sessions: 3, BytesBehind: 4096},
			want:  []string{"Session files: 3 session(s) registered; last read never; 4.0 KiB not read yet."},
		},
		{
			name: "read with nothing left",
			state: report.SessionFilesState{
				Sessions:   2,
				LastReadAt: time.Date(2026, 9, 10, 8, 30, 0, 0, time.UTC),
			},
			want: []string{"Session files: 2 session(s) registered; last read 2026-09-10T08:30:00Z; 0 B not read yet."},
		},
		{
			name:  "the tree was larger than the search visits",
			state: report.SessionFilesState{Sessions: 1, Gaps: follow.Gaps{Truncated: true}},
			want:  []string{treeLimitLine},
		},
		{
			name:  "a directory whose name another one shares",
			state: report.SessionFilesState{Sessions: 1, Gaps: follow.Gaps{Ambiguous: []follow.Ambiguity{ambiguity}}},
			want: []string{
				"Not collected: /home/dev/sample-project/a-b stores its session files under the same name as /home/dev/sample-project/a_b, and they cannot be told apart.",
			},
		},
		{
			name:  "a directory that could not be listed",
			state: report.SessionFilesState{Sessions: 1, Gaps: follow.Gaps{Unreadable: []string{"/home/dev/sample-project/locked"}}},
			want:  []string{"Not looked at: /home/dev/sample-project/locked could not be listed, nor anything below it."},
		},
		{
			name:   "a registry that cannot be read",
			state:  report.SessionFilesState{Err: errors.New("permission denied")},
			want:   []string{"WARNING: the session file registry could not be read: permission denied. Run `trajector doctor`."},
			reject: []string{"Session files:"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := enabledDevice()
			d.SessionFiles = tc.state
			out := dashboard(d)
			wants(t, "status", out, tc.want...)
			rejects(t, "status", out, tc.reject...)
		})
	}
}

func TestStatusSaysNothingAboutSessionFilesOfAProjectNotEnabled(t *testing.T) {
	d := device()
	d.SessionFiles = report.SessionFilesState{Sessions: 4}
	rejects(t, "status", dashboard(d), "Session files", spellingLine, hooksWillLoadLine, remoteControlLine)
}

func TestStatusJudgesTheHooksOfEveryEnabledProject(t *testing.T) {
	notLoading := &claudesettings.HookPolicy{Reason: "disableAllHooks in /home/dev/.claude/settings.json"}
	for _, tc := range []struct {
		name    string
		shape   func(report.Diagnosis) report.Diagnosis
		policy  *claudesettings.HookPolicy
		want    []string
		reject  []string
		problem bool
	}{
		{
			name:   "hooks load",
			shape:  func(d report.Diagnosis) report.Diagnosis { return d },
			policy: &claudesettings.HookPolicy{Runs: true},
			want:   []string{hooksWillLoadLine},
			reject: []string{hooksWillNotLoadLine, proxyHalfOnlyLine, nothingRecordedLine},
		},
		{
			name:   "hooks will not load beside a base URL",
			shape:  func(d report.Diagnosis) report.Diagnosis { return d },
			policy: notLoading,
			want:   []string{hooksWillNotLoadLine + " (disableAllHooks in /home/dev/.claude/settings.json)", proxyHalfOnlyLine},
			reject: []string{nothingRecordedLine},
		},
		{
			name:   "hooks will not load without a base URL",
			shape:  withoutProxy,
			policy: notLoading,
			want:   []string{hooksWillNotLoadLine + " (disableAllHooks in /home/dev/.claude/settings.json)", nothingRecordedLine, "Run trajector enable without --no-proxy"},
			reject: []string{proxyHalfOnlyLine},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.shape(enabledDevice())
			d.HookPolicy = tc.policy
			out := dashboard(d)
			wants(t, "status", out, tc.want...)
			rejects(t, "status", out, tc.reject...)
		})
	}
}

func TestStatusSaysWhatEachShapeCosts(t *testing.T) {
	t.Run("with a base URL", func(t *testing.T) {
		d := enabledDevice()
		d.Project.Upstream = "https://relay.example.com"
		out := dashboard(d)
		wants(t, "status", out, remoteControlLine, "https://relay.example.com", "third-party")
		rejects(t, "status", out, noProxyFactLine)
	})
	t.Run("without a base URL", func(t *testing.T) {
		d := withoutProxy(enabledDevice())
		d.Project.Upstream = "https://relay.example.com"
		out := dashboard(d)
		wants(t, "status", out, "Contributing", noProxyFactLine)
		// No traffic of the project's reaches the proxy, so there is no
		// upstream to speak of, third-party or not.
		rejects(t, "status", out, remoteControlLine, "relay.example.com", "third-party", "Upstream")
	})
}

func TestStatusNamesAMissingSessionEndHookWhileStillContributing(t *testing.T) {
	d := enabledDevice()
	d.Project.SessionEndInstalled = false
	out := dashboard(d)
	wants(t, "status", out, "Contributing",
		"The session-end hook is missing from /home/dev/sample-project/.claude/settings.local.json; run `trajector doctor` to add it.")
	rejects(t, "status", out, "disagree")
}

func TestStatusReportsClaudeOnTheWindowsSideAsAFact(t *testing.T) {
	d := enabledDevice()
	d.Project.WindowsSideClaude = true
	wants(t, "status", dashboard(d), "Windows side", "cannot reach this trajector", "`trajector doctor`")
}

func TestStatusTellsTheTwoRecordingPausesApart(t *testing.T) {
	agreement := device()
	agreement.Project.PauseReason = routing.PauseConsentReconfirm
	redaction := device()
	redaction.Project.PauseReason = routing.PauseRedactionDrift

	a, b := dashboard(agreement), dashboard(redaction)
	wants(t, "status", a, "Recording is paused everywhere", "the data agreement changed", "`trajector enable`")
	wants(t, "status", b, "Recording is paused everywhere", "redaction does not cover", "`trajector upgrade`")
	paused := enabledDevice()
	paused.Project.PauseReason = routing.PauseRedactionDrift
	wants(t, "status", dashboard(paused), "Contributing; recording is paused for now (see Device above).")
	rejects(t, "status", dashboard(paused), "recording is on for this project")
	rejects(t, "status", a, "redaction")
	rejects(t, "status", b, "agreement")
	if a == b {
		t.Fatal("the two pauses render alike")
	}
}

func TestDoctorTellsTheTwoRecordingPausesApart(t *testing.T) {
	d := device()
	d.Project.PauseReason = routing.PauseRedactionDrift
	problems, out := doctorText(d)
	if problems != 1 {
		t.Errorf("problems = %d, want the pause counted once", problems)
	}
	wants(t, "doctor", out, "problem: recording is paused everywhere", "redaction does not cover", "`trajector upgrade`")
	rejects(t, "doctor", out, "agreement")
}

func TestStatusShowsHowFarBehindUploadingTheRecordsAre(t *testing.T) {
	d := device()
	rejects(t, "status", dashboard(d), "segment(s)")
	wants(t, "status", dashboard(d), "Records waiting to upload: none.")

	d.Spool.Days = []spool.DaySummary{
		{Day: "20260909", Count: spool.Count{Segments: 2, Snapshots: 1}},
		{Day: "20260910", Count: spool.Count{Segments: 3}},
	}
	d.Spool.OldestRecord = time.Date(2026, 9, 9, 7, 0, 0, 0, time.UTC)
	wants(t, "status", dashboard(d), "Records waiting to upload: 5 segment(s), 1 snapshot(s); the oldest is from 2026-09-09T07:00:00Z.")

	d.Spool = report.SpoolState{Dir: spoolDir, OpenErr: errors.New("not a directory")}
	rejects(t, "status", dashboard(d), "waiting to upload")
}

func TestStatusDoesNotCallAnIdleProxyAFaultWhereNoProjectUsesIt(t *testing.T) {
	d := withoutProxy(enabledDevice())
	d.ProxyIdleBetweenSessions = true
	out := dashboard(d)
	wants(t, "status", out, "Not running; on this device it runs only while a session is open")
	rejects(t, "status", out, "WARNING", "starts on demand")
}

func TestDoctorSendsAnUntrustedWorkspaceToTheDialog(t *testing.T) {
	d := enabledDevice()
	problems, out := doctorProjectText(walked(d, 2))
	if problems != 1 {
		t.Errorf("problems = %d, want the untrusted workspace counted once", problems)
	}
	wants(t, "doctor", out, "problem: "+workspaceTrustLine, "2 session(s) of this project were written without a hook of trajector's reporting them")
	rejects(t, "doctor", out, ".jsonl", remoteControlLine)
}

func TestDoctorPassesWhenEverySessionFileIsRegistered(t *testing.T) {
	d := enabledDevice()
	d.SessionFiles.Sessions = 3
	problems, out := doctorProjectText(walked(d, 0))
	if problems != 0 {
		t.Errorf("problems = %d, want none", problems)
	}
	wants(t, "doctor", out, "ok: "+hooksWillLoadLine, "ok: every session file of this project is registered (3 session(s))")
	rejects(t, "doctor", out, workspaceTrustLine, "Remote Control")
}

func TestDoctorExplainsHooksThatWillNotLoadWithoutFailing(t *testing.T) {
	policy := &claudesettings.HookPolicy{Reason: "disableAllHooks in /home/dev/.claude/settings.json"}
	for _, tc := range []struct {
		name  string
		shape func(report.Diagnosis) report.Diagnosis
		want  []string
	}{
		{"with a base URL", func(d report.Diagnosis) report.Diagnosis { return d }, []string{proxyHalfOnlyLine + "."}},
		{"without a base URL", withoutProxy, []string{nothingRecordedLine + ".", "trajector enable without --no-proxy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.shape(enabledDevice())
			d.HookPolicy = policy
			problems, out := doctorProjectText(walked(d, 1))
			if problems != 0 {
				t.Errorf("problems = %d, want a setting the user or their organization keeps not counted as a fault", problems)
			}
			wants(t, "doctor", out,
				"note: "+hooksWillNotLoadLine+" (disableAllHooks in /home/dev/.claude/settings.json)",
				"Change that setting where it is set, or ask whoever manages it to.",
				"1 session(s) of this project were written without a hook of trajector's reporting them, which follows from the setting above")
			wants(t, "doctor", out, tc.want...)
			rejects(t, "doctor", out, workspaceTrustLine, "Remote Control")
		})
	}
}

func TestNoSurfaceWordsTheHookReadingDifferently(t *testing.T) {
	loading := &claudesettings.HookPolicy{Runs: true}
	locked := &claudesettings.HookPolicy{Reason: "disableAllHooks in /home/dev/.claude/settings.json"}
	sameShape := func(d report.Diagnosis) report.Diagnosis { return d }
	for _, tc := range []struct {
		name   string
		shape  func(report.Diagnosis) report.Diagnosis
		policy *claudesettings.HookPolicy
	}{
		{"hooks load beside a base URL", sameShape, loading},
		{"hooks load without a base URL", withoutProxy, loading},
		{"hooks will not load beside a base URL", sameShape, locked},
		{"hooks will not load without a base URL", withoutProxy, locked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.shape(enabledDevice())
			d.HookPolicy = tc.policy
			outlook := report.ExplainHooks(*tc.policy, d.Project.Shape)
			wants(t, "status", dashboard(d), outlook.Lines()...)
			_, out := doctorProjectText(walked(d, 0))
			wants(t, "doctor", out, outlook.Judgement)
		})
	}
}

func TestDoctorReportsClaudeOnTheWindowsSideWithAWayOut(t *testing.T) {
	d := enabledDevice()
	d.Project.Root = "/mnt/c/Users/dev/sample-project"
	d.Project.WindowsSideClaude = true
	problems, out := doctorProjectText(d)
	if problems != 1 {
		t.Errorf("problems = %d, want the arrangement counted once and the unreported sessions not counted again", problems)
	}
	wants(t, "doctor", out,
		"problem: this project is on a Windows drive mounted into WSL",
		"Run both on the same side")
	rejects(t, "doctor", out, workspaceTrustLine, "every session file of this project is registered")
}

func TestDoctorStatesWhatTheSearchCouldNotCover(t *testing.T) {
	d := enabledDevice()
	d.SessionFiles.Gaps = follow.Gaps{
		Truncated:  true,
		Ambiguous:  []follow.Ambiguity{{Dir: "/home/dev/sample-project/a-b", Name: "n", Matches: []string{"/home/dev/sample-project/a-b", "/home/dev/sample-project/a_b"}}},
		Unreadable: []string{"/home/dev/sample-project/locked"},
	}
	problems, out := doctorProjectText(walked(d, 0))
	if problems != 0 {
		t.Errorf("problems = %d, want what the search could not cover stated, not counted", problems)
	}
	wants(t, "doctor", out,
		"note: "+treeLimitLine,
		"note: Not collected: /home/dev/sample-project/a-b stores its session files under the same name as /home/dev/sample-project/a_b",
		"note: Not looked at: /home/dev/sample-project/locked could not be listed")
}

func TestDoctorReportsASearchOrARegistryItCouldNotRead(t *testing.T) {
	d := enabledDevice()
	d.SessionFiles.WalkErr = errors.New("root is not absolute")
	_, out := doctorProjectText(d)
	wants(t, "doctor", out, "problem: could not look for this project's session files: root is not absolute")

	d.SessionFiles = report.SessionFilesState{Err: errors.New("permission denied")}
	_, out = doctorProjectText(d)
	wants(t, "doctor", out, "problem: the session file registry could not be read: permission denied")
}

func TestDoctorSaysNothingAboutAProjectNotEnabled(t *testing.T) {
	d := device()
	d.HookPolicy = &claudesettings.HookPolicy{Runs: true}
	_, out := doctorProjectText(walked(d, 5))
	if out != "" {
		t.Errorf("doctor = %q, want nothing about a project that is not enabled", out)
	}
}

func TestTheBundleCarriesShapeAndSessionCountsWithoutIdsOrPaths(t *testing.T) {
	d := withoutProxy(enabledDevice())
	d.Project.SessionEndInstalled = false
	d.HookPolicy = &claudesettings.HookPolicy{Reason: "disableAllHooks in /home/dev/.claude/settings.json"}
	d.SessionFiles = report.SessionFilesState{
		Sessions:    4,
		LastReadAt:  time.Date(2026, 9, 10, 8, 30, 0, 0, time.UTC),
		BytesBehind: 512,
		Gaps: follow.Gaps{
			Truncated:  true,
			Ambiguous:  []follow.Ambiguity{{Dir: "/home/dev/sample-project/a-b", Name: "n", Matches: []string{"/home/dev/sample-project/a-b"}}},
			Unreadable: []string{"/home/dev/sample-project/locked"},
		},
		Walked:       true,
		Unregistered: 2,
	}
	d.Spool.Days = []spool.DaySummary{{Day: "20260910", Count: spool.Count{Segments: 2}}}
	d.Spool.OldestRecord = time.Date(2026, 9, 10, 7, 0, 0, 0, time.UTC)

	got := string(report.DiagnosisJSON(d))
	wants(t, "diagnosis.json", got,
		`"no_proxy": true`,
		`"session_end_installed": false`,
		`"runs": false`,
		`"reason": "disableAllHooks in /home/dev/.claude/settings.json"`,
		`"sessions": 4`,
		`"last_read_at": "2026-09-10T08:30:00Z"`,
		`"bytes_behind": 512`,
		`"truncated": true`,
		`"ambiguous": 1`,
		`"unreadable": 1`,
		`"walked": true`,
		`"unregistered": 2`,
		`"oldest_record_at": "2026-09-10T07:00:00Z"`,
		`"segments": 2`,
	)
	rejects(t, "diagnosis.json", got, "/a-b", "/locked", ".jsonl")
	if strings.Contains(got, `"hook_policy": null`) {
		t.Errorf("diagnosis.json = %s, want no null hook policy", got)
	}
}

func TestStatusStatesWhatReadingNoticedAsCountsAndFieldNames(t *testing.T) {
	alerts := drift.Signals{
		AssistantLines:                      12,
		AssistantLinesWithoutMessageID:      3,
		AssistantLinesMissingResponseFields: 4,
		MessagesWithBlockIndexGap:           2,
		AgentLines:                          5,
		AgentLinesWithoutParent:             1,
	}
	stopped := drift.Signals{
		UnanchoredPathFields: []string{"$.attachment.snapshot.newDir", "$.someNewPath"},
		IncompleteSegments:   1,
		NewTopLevelTypes:     []string{"mood-ring"},
		NewLaunchSurfaces:    []string{"claude-holodeck"},
	}
	for _, tc := range []struct {
		name    string
		signals drift.Signals
		pause   routing.PauseReason
		want    []string
		reject  []string
	}{
		{
			name:    "nothing noticed",
			signals: drift.Signals{AssistantLines: 12, AgentLines: 5},
			reject:  []string{"carried no message id", "block indexes", "started by", "redaction does not cover"},
		},
		{
			name:    "lines lacking what a record is read by",
			signals: alerts,
			want: []string{
				"3 of 12 assistant lines in this project's session files carried no message id.",
				"4 of 12 assistant lines in this project's session files lacked a response field a record is read by.",
				"2 message(s) in this project's session files had block indexes that repeat or skip a number.",
				"1 of 5 agent lines in this project's session files named nothing they were started by.",
			},
			reject: []string{"trajector doctor` to", "redaction does not cover"},
		},
		{
			name:    "fields named while recording is paused for them",
			signals: stopped,
			pause:   routing.PauseRedactionDrift,
			want: []string{
				"Fields in this project's session files holding a path that this build's redaction does not cover: $.attachment.snapshot.newDir, $.someNewPath.",
				"1 read(s) of this project's session files ended in a line without a newline.",
			},
			reject: []string{"mood-ring", "claude-holodeck"},
		},
		{
			name:    "fields not named once the pause is lifted",
			signals: stopped,
			reject:  []string{"$.someNewPath", "without a newline", "mood-ring"},
		},
		{
			name:    "fields not named under the other pause",
			signals: stopped,
			pause:   routing.PauseConsentReconfirm,
			reject:  []string{"$.someNewPath", "without a newline"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := enabledDevice()
			d.Project.PauseReason = tc.pause
			d.SessionFiles.Signals = tc.signals
			out := dashboard(d)
			wants(t, "status", out, tc.want...)
			rejects(t, "status", out, tc.reject...)
		})
	}
}

func TestTheBundleCarriesWhatReadingNoticedWithoutValues(t *testing.T) {
	d := enabledDevice()
	d.SessionFiles.Signals = drift.Signals{
		UnanchoredPathFields:           []string{"$.someNewPath"},
		AssistantLines:                 4,
		AssistantLinesWithoutMessageID: 1,
		NewLaunchSurfaces:              []string{"claude-holodeck"},
	}
	got := string(report.DiagnosisJSON(d))
	wants(t, "diagnosis.json", got,
		`"unanchored_path_fields": [`,
		`"$.someNewPath"`,
		`"assistant_lines": 4`,
		`"assistant_lines_without_message_id": 1`,
		`"new_launch_surfaces": [`,
	)
	rejects(t, "diagnosis.json", got, `"agent_lines"`, `"incomplete_segments"`)

	d.SessionFiles.Signals = drift.Signals{}
	if got := string(report.DiagnosisJSON(d)); strings.Contains(got, `"signals"`) {
		t.Errorf("diagnosis.json = %s, want no signals key when nothing was noticed", got)
	}
}
