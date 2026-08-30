package report_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
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
	rejects(t, "status", out, "WARNING")
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
		{"signed out", routing.PauseSignedOut, []string{"paused", "`trajector login`"}},
		{"agreement needs reconfirming", routing.PauseConsentReconfirm, []string{"paused", "`trajector enable`"}},
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

	wants(t, "status", out, "WARNING", "`trajector doctor`")
}

func TestStatusReportsAForeignPortHolder(t *testing.T) {
	d := device()
	d.Proxy = foreign(proxylife.ErrPortOccupied)
	out := dashboard(d)

	wants(t, "status", out, "WARNING", "not the trajector proxy", "find and stop the process")
	rejects(t, "status", out, "Running at")
}

func TestStatusPresentsAnUnverifiableProxyAsAuthentication(t *testing.T) {
	d := device()
	d.Proxy = foreign(proxylife.ErrProxyUnverified)
	out := dashboard(d)

	wants(t, "status", out, "WARNING", "could not verify the proxy", "authentication problem")
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
	wants(t, "status", out, "Last upload: 3 rawcall(s)", "2026-08-02T10:00:00Z", "boom")
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
		"WARNING", "2 rawcall(s)", "1 rejected batch(es)",
		"not be retried automatically", "`trajector doctor`")
}

func TestStatusRendersEverySectionWhenTheSpoolCannotOpen(t *testing.T) {
	d := device()
	d.Spool = report.SpoolState{Dir: spoolDir, OpenErr: errors.New("not a directory")}
	d.Handshake.Notice = "scheduled maintenance on Friday"
	out := dashboard(d)

	wants(t, "status", out,
		"the capture spool at "+spoolDir+" is not usable",
		"Uploads", "Never uploaded", "scheduled maintenance on Friday", "`trajector doctor`")
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
		"`trajector doctor`")
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

// contributing is a project in the fully healthy enabled state.
func contributing() report.ProjectStatus {
	return report.ProjectStatus{
		Root:            "/home/dev/sample-project",
		Hash:            "hash-p1",
		Enabled:         true,
		Token:           "tok-1",
		Upstream:        "https://api.anthropic.com",
		InjectedBaseURL: "http://127.0.0.1:41100/t/tok-1",
		InjectedToken:   "tok-1",
		HookInstalled:   true,
	}
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
