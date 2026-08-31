package report

import (
	"fmt"
	"io"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
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
	case st.Consistent():
		fmt.Fprintln(w, "  Contributing; recording is on for this project.")
		if st.Upstream != capture.Anthropic.OfficialUpstream {
			fmt.Fprintf(w, "  Upstream: %s (third-party origin).\n", st.Upstream)
		}
		if st.UpstreamMoved.Happened() {
			fmt.Fprintf(w, "  The upstream moved from %s at %s (base-URL configuration change).\n", st.UpstreamMoved.From, st.UpstreamMoved.At)
		}
		for _, line := range optionalSettingLines(d.OptionalSettings) {
			fmt.Fprintf(w, "  %s\n", line)
		}
	case !st.Enabled && !st.Injected():
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
		fmt.Fprintln(w, "  Not running; it starts on demand with the next session.")
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
		fmt.Fprintf(w, "  Last upload: %d rawcall(s) (%s) at %s.\n",
			r.Records, platform.HumanBytes(r.Bytes), r.At.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintln(w, "  Never uploaded.")
	}
	if d.Uploads.LastError != "" {
		fmt.Fprintf(w, "  Last error: %s (%s).\n", d.Uploads.LastError, d.Uploads.LastErrorAt.UTC().Format(time.RFC3339))
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
			fmt.Fprintf(w, "  "+ServiceSays+"\n", s.Message)
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
