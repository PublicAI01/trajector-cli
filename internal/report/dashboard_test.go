package report_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

func TestStatusOnAFreshDevice(t *testing.T) {
	d := device()
	d.TokenStore.Paired = false
	out := dashboard(d)

	wants(t, "status", out,
		"Not signed in",
		"`trajector login`",
		"Not enabled",
		"`trajector enable`",
		"Not running",
		"0 B of 2.0 GiB used",
		"Never uploaded",
	)
	rejects(t, "status", out, "error:", "warning:")
}

func TestStatusShowsAnEnabledProjectAndRunningProxy(t *testing.T) {
	d := device()
	d.Project = contributing()
	d.Proxy = ours("testv")
	out := dashboard(d)

	wants(t, "status", out,
		"Signed in",
		"Contributing",
		"Running at "+d.Proxy.Addr,
		"version testv",
		// Named for the span it counts — this proxy's run, the one the
		// uptime on the line above measures — not for a calendar day
		// no restart respects.
		"Recorded since it started: 0",
	)
	rejects(t, "status", out, "third-party")
}

// The client states the rule and no figure, so the sentence is only
// useful if it also says where the figures are. A contributing project
// carries both.
func TestStatusSendsTheUserToWhereTheRatesArePublished(t *testing.T) {
	d := device()
	d.Project = contributing()
	out := dashboard(d)

	wants(t, "status", out,
		"Calls the local proxy did not witness are rewarded at a lower rate",
		"The current rates are published at "+report.RewardsDoc+".",
	)
}

func TestStatusLabelsAThirdPartyUpstream(t *testing.T) {
	d := device()
	d.Project = contributing()
	d.Project.Upstream = "https://relay.example.com"
	out := dashboard(d)

	wants(t, "status", out, "https://relay.example.com", "third-party")
}

func TestStatusExplainsADeviceWidePause(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason routing.PauseReason
		want   []string
	}{
		{"signed out", routing.PauseSignedOut, []string{"paused", "fix:  trajector login"}},
		{"agreement needs reconfirming", routing.PauseConsentReconfirm, []string{"paused", "fix:  trajector enable"}},
		// A pause reason this build does not know (say, written by a
		// newer one) must still be shown, not hidden.
		{"unrecognized", "some_future_reason", []string{"some_future_reason"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := device()
			d.Project.PauseReason = tc.reason
			wants(t, "status", dashboard(d), tc.want...)
		})
	}
}

func TestStatusWarnsWhenInjectionAndRoutingDisagree(t *testing.T) {
	// A grant with no matching injection: the routing table says this
	// project contributes, the settings say nothing routes here.
	d := device()
	d.Project.Enabled = true
	d.Project.Token = "tok-orphaned-grant"
	out := dashboard(d)

	wants(t, "status", out, "warning: ", "`trajector doctor`")
}

func TestStatusReportsAPortHeldByAnotherProcess(t *testing.T) {
	for _, tc := range []struct {
		name string
		why  error
		want string
	}{
		{
			name: "the holder answered without proof",
			why:  &proxylife.PortHeld{Addr: "127.0.0.1:41100", Why: proxylife.ErrPortOccupied},
			want: "not the trajector proxy",
		},
		{
			name: "the holder answered nothing",
			why:  &proxylife.PortHeld{Addr: "127.0.0.1:41100", Why: proxylife.ErrPortSilent, Observed: "context deadline exceeded"},
			want: "did not answer a probe",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := device()
			d.Proxy = foreign(tc.why)
			out := dashboard(d)

			wants(t, "status", out,
				"error: ", tc.want,
				"why:  another process holds the proxy port",
				"free the port by stopping whatever holds it",
				"To find the holder: ",
				"41100",
				"Once the port is free, run `trajector doctor`.")
			// No command of trajector's takes a port from another
			// process, so none is offered on the line meant to be
			// copied.
			rejects(t, "status", out, "fix:  ", "Running at")
		})
	}
}

func TestStatusNamesTheHolderItCouldReadAndNothingMore(t *testing.T) {
	d := device()
	d.Proxy = foreign(&proxylife.PortHeld{Addr: "127.0.0.1:41100", Why: proxylife.ErrPortSilent})
	d.ProxyHolder = report.HolderProcess{PID: 4321, Name: "example-helper"}
	out := dashboard(d)

	wants(t, "status", out, "At the port as this device reads it: example-helper (pid 4321).")
}

func TestStatusSaysNothingAboutAHolderItCouldNotRead(t *testing.T) {
	d := device()
	d.Proxy = foreign(&proxylife.PortHeld{Addr: "127.0.0.1:41100", Why: proxylife.ErrPortSilent})
	out := dashboard(d)

	rejects(t, "status", out, "At the port as this device reads it")
}

func TestStatusPresentsAnUnverifiableProxyAsAuthentication(t *testing.T) {
	d := device()
	d.Proxy = foreign(proxylife.ErrProxyUnverified)
	out := dashboard(d)

	wants(t, "status", out, "error: ", "could not verify the proxy", "authentication problem")
	// Never advise hunting a process that may be our own proxy.
	rejects(t, "status", out, "find and stop the process")
}

func TestStatusShowsSpoolUsageAndLastUpload(t *testing.T) {
	d := device()
	d.Spool.Usage = 4096
	at := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	d.Uploads = upload.State{
		LastUpload:  &upload.Receipt{BatchID: "b-1", Records: 3, Bytes: 2048, At: at},
		LastError:   "boom",
		LastErrorAt: at.Add(time.Hour),
	}
	out := dashboard(d)

	rejects(t, "status", out, "0 B of")
	wants(t, "status", out, "Last upload: 3 record(s)", "2026-08-02T10:00:00Z", "boom")
}

func TestStatusWarnsAboutRejectedBatches(t *testing.T) {
	d := device()
	d.Rejected = []upload.RejectedBatch{{
		BatchID: "b-poison",
		Records: 2,
		Reason:  upload.Rejection{Details: "413 Request Entity Too Large"},
	}}
	out := dashboard(d)

	wants(t, "status", out,
		"error: ", "2 record(s)", "1 rejected batch(es)",
		"not be retried automatically", "fix:  trajector doctor requeue")
}

func TestStatusCountsWaitingRawcallsOnADeviceThatRecordsOnlyThroughTheProxy(t *testing.T) {
	d := device()
	d.Spool.Days = []spool.DaySummary{{Day: "20260909", Count: spool.Count{envelope.KindRawcall: 2}}, {Day: "20260910", Count: spool.Count{envelope.KindRawcall: 1}}}
	d.Spool.OldestRecord = time.Date(2026, 9, 9, 7, 0, 0, 0, time.UTC)
	out := dashboard(d)

	wants(t, "status", out, "Records waiting to upload: 3 rawcall(s); the oldest is from 2026-09-09T07:00:00Z.")
	rejects(t, "status", out, "waiting to upload: none", "segment(s)", "session snapshot(s)")
}

func TestStatusCountsEveryKindWaitingOnADeviceThatRecordsBothWays(t *testing.T) {
	d := device()
	d.Spool.Days = []spool.DaySummary{{Day: "20260909", Count: spool.Count{envelope.KindRawcall: 2, envelope.KindSegment: 3, envelope.KindMetaSnapshot: 1, envelope.KindGitSnapshot: 4}}}
	out := dashboard(d)

	wants(t, "status", out, "Records waiting to upload: 2 rawcall(s), 3 segment(s), 1 session snapshot(s), 4 git snapshot(s).")
}

func TestStatusCountsWaitingGitSnapshotsWhenNothingElseWaits(t *testing.T) {
	d := device()
	d.Spool.Days = []spool.DaySummary{{Day: "20260909", Count: spool.Count{envelope.KindGitSnapshot: 4}}}
	out := dashboard(d)

	wants(t, "status", out, "Records waiting to upload: 4 git snapshot(s).")
	rejects(t, "status", out, "waiting to upload: none")
}

func TestStatusRendersEverySectionWhenTheSpoolCannotOpen(t *testing.T) {
	d := device()
	d.Spool = report.SpoolState{Dir: spoolDir, OpenErr: errors.New("not a directory")}
	d.Handshake.Notice = "scheduled maintenance on Friday"
	out := dashboard(d)

	wants(t, "status", out,
		"the capture spool at "+spoolDir+" is not usable",
		"Uploads", "Never uploaded", "scheduled maintenance on Friday", "fix:  trajector doctor")
	// No writability verdict for a spool that never opened.
	rejects(t, "status", out, "full", "not writable")
}

func TestStatusShowsRejectedBatchesAlongsideASpoolError(t *testing.T) {
	d := device()
	d.Spool = report.SpoolState{Dir: spoolDir, OpenErr: errors.New("not a directory")}
	d.Rejected = []upload.RejectedBatch{{BatchID: "b-poison", Records: 1}}
	out := dashboard(d)

	wants(t, "status", out, "is not usable", "1 rejected batch(es)", "not be retried automatically")
}

func TestStatusWarnsWhenTheRejectedBatchesCannotBeRead(t *testing.T) {
	d := device()
	d.RejectedErr = errors.New("not a directory")
	out := dashboard(d)

	wants(t, "status", out,
		"the rejected batches at "+rejectedDir+" could not be read",
		"fix:  trajector doctor")
}

// Every reason uploads are held back reads the same way: what is true,
// then the service's own words, then what ends it.
func TestStatusPrintsEveryStandingWithTheServicesWordsBetween(t *testing.T) {
	d := device()
	d.Standings = []upload.Standing{
		{Reason: upload.VersionGate, MinClientVersion: "9.9.9", Version: "0.1.0", Message: "Upload format 0.1.x is retired on 2026-09-01.", Upgradable: true},
		{Reason: upload.AuthorizationGate, AuthorizeURL: "https://dashboard.example.com/authorization"},
	}
	out := dashboard(d)

	for _, s := range d.Standings {
		wants(t, "status", out, s.Explain(), s.Remedy())
	}
	wants(t, "status", out, "The service says: Upload format 0.1.x is retired on 2026-09-01.")
	if explain, says := strings.Index(out, d.Standings[0].Explain()), strings.Index(out, "The service says:"); explain > says {
		t.Errorf("status = %q, want the standing's own sentence before the service's words", out)
	}
}

// The Project section of a contributing project ends with the optional
// settings. Three of the lines are finalized wording; the fourth — a
// true of the user's own — is the same statement without the
// set-by-trajector mark.
func TestStatusEndsTheProjectSectionWithTheOptionalSetting(t *testing.T) {
	const key = claudesettings.KeyShowThinkingSummaries
	recommendation := "One optional setting is off: " + key + ". " +
		"Turning it on costs you nothing and makes your records more complete. " +
		"Run `trajector enable` to see what it changes."
	for _, tc := range []struct {
		name    string
		setting report.OptionalSettingStatus
		want    string
		reject  []string
	}{
		{
			name:    "off and never decided",
			setting: report.OptionalSettingStatus{Key: key, State: claudesettings.Unset},
			want:    recommendation,
			reject:  []string{"declined", "set by trajector"},
		},
		{
			name:    "explicitly off but never declined here",
			setting: report.OptionalSettingStatus{Key: key, State: claudesettings.OffByUser},
			want:    recommendation,
			reject:  []string{"declined", "set by trajector"},
		},
		{
			name:    "declined",
			setting: report.OptionalSettingStatus{Key: key, State: claudesettings.Unset, Declined: true},
			want:    "Optional settings: 1 declined. Run `trajector enable` to review.",
			reject:  []string{"costs you nothing", "set by trajector"},
		},
		{
			name:    "on because trajector wrote it",
			setting: report.OptionalSettingStatus{Key: key, State: claudesettings.OnByUs},
			want:    "Optional settings: " + key + " on (set by trajector).",
			reject:  []string{"costs you nothing", "declined"},
		},
		{
			name:    "on as the user's own choice",
			setting: report.OptionalSettingStatus{Key: key, State: claudesettings.OnByUser},
			want:    "Optional settings: " + key + " on.",
			reject:  []string{"costs you nothing", "declined", "set by trajector"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := device()
			d.Project = contributing()
			d.OptionalSettings = []report.OptionalSettingStatus{tc.setting}
			out := dashboard(d)

			wants(t, "status", out, tc.want)
			rejects(t, "status", out, tc.reject...)
		})
	}
}

func TestStatusShowsNoOptionalSettingLineOutsideAContributingProject(t *testing.T) {
	d := device()
	d.OptionalSettings = []report.OptionalSettingStatus{
		{Key: claudesettings.KeyShowThinkingSummaries, State: claudesettings.Unset},
	}
	out := dashboard(d)

	rejects(t, "status", out, "optional setting", "Optional settings", claudesettings.KeyShowThinkingSummaries)
}

// contributing is a project in the fully healthy enabled state.
func contributing() report.ProjectStatus {
	return report.ProjectStatus{
		Root:            "/home/dev/sample-project",
		Hash:            "hash-p1",
		Enabled:         true,
		Token:           "tok-1",
		Upstream:        "https://api.anthropic.com",
		Shape:           routing.WithProxy,
		InjectedBaseURL: "http://127.0.0.1:41100/t/tok-1",
		InjectedToken:   "tok-1",
		Injected:        true,
		InjectionAgrees: true,
		Hooks:           everyProjectHook(),
	}
}

// everyProjectHook is the settings file of a project carrying every
// hook this release installs; hooksWithout is the same file with one
// hook of it never installed.
func everyProjectHook() claudesettings.InstalledHooks {
	return claudesettings.InstalledHooks(claudesettings.ProjectHookSubcommands())
}

func hooksWithout(subcommand string) claudesettings.InstalledHooks {
	return slices.DeleteFunc(everyProjectHook(), func(installed string) bool { return installed == subcommand })
}

// ours is a verdict about a proxy of this device's own.
func ours(version string) proxylife.Verdict {
	return proxylife.Verdict{
		Addr:   "127.0.0.1:41100",
		Holder: proxylife.HolderOurs,
		Health: apiproxy.Health{Service: apiproxy.ServiceName, Version: version, UptimeSeconds: 12},
	}
}

// foreign is a verdict about a port holder that could not be proven
// ours, carrying the reason the proof failed.
func foreign(why error) proxylife.Verdict {
	return proxylife.Verdict{
		Addr:   "127.0.0.1:41100",
		Holder: proxylife.HolderForeign,
		Reason: why,
	}
}

// full is a spool refusing writes because usage reached the quota, the
// one writability failure with a remedy of its own.
func full() report.SpoolState {
	return report.SpoolState{
		Dir:         spoolDir,
		Usage:       2 << 30,
		Quota:       2 << 30,
		WritableErr: spool.ErrQuotaExceeded,
	}
}

// waitingLine returns what status says is waiting, without its prefix.
func waitingLine(t *testing.T, out string) string {
	t.Helper()
	const prefix = "Records waiting to upload: "
	for line := range strings.SplitSeq(out, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			return after
		}
	}
	t.Fatalf("status says nothing about records waiting:\n%s", out)
	return ""
}

func TestStatusHasAWordForEveryRecordKindItCanCount(t *testing.T) {
	for _, kind := range envelope.Kinds() {
		t.Run(kind.CountKey(), func(t *testing.T) {
			d := device()
			d.Spool.Days = []spool.DaySummary{{Day: "20260909", Count: spool.Count{kind: 1}}}
			noun, counted := strings.CutPrefix(waitingLine(t, dashboard(d)), "1 ")
			noun = strings.TrimSuffix(noun, ".")
			if !counted || noun == "" || noun == kind.CountKey() {
				t.Errorf("one %s record is reported as %q, want a word written for the user", kind.CountKey(), noun)
			}
		})
	}
}
