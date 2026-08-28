package lifecycle

import (
	"fmt"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
)

// Status prints the device dashboard: pairing, the current project's
// consent, the proxy, the spool, uploads, and what the service last
// said. It renders a Diagnosis and nothing else — it never repairs
// anything, always leaves the fixing to doctor, and never starts a
// proxy just to look at one. A store that could not be read is that
// section's warning, never a reason to cut the sections after it.
func (m *Machine) Status(dir string, io IO) error {
	fmt.Fprintf(io.Out, "trajector %s\n", m.deps.Version)

	d, err := m.Diagnose(dir)
	if err != nil {
		return err
	}
	st := d.Project

	fmt.Fprintln(io.Out, "\nDevice")
	switch {
	case d.TokenStore.Err != nil:
		fmt.Fprintln(io.Out, "  WARNING: the device token store could not be read. Run `trajector doctor`.")
	case d.TokenStore.Paired:
		fmt.Fprintln(io.Out, "  Signed in.")
	default:
		fmt.Fprintln(io.Out, "  Not signed in. Run `trajector login` to pair this device.")
	}
	if st.PauseReason != "" {
		fmt.Fprintf(io.Out, "  Recording is paused everywhere: %s.\n", st.PauseReason.Explain())
	}

	fmt.Fprintf(io.Out, "\nProject %s\n", st.Root)
	switch {
	case st.Consistent():
		fmt.Fprintln(io.Out, "  Contributing; recording is on for this project.")
		if st.Upstream != capture.Anthropic.OfficialUpstream {
			fmt.Fprintf(io.Out, "  Upstream: %s (third-party origin).\n", st.Upstream)
		}
		if st.UpstreamMoved.Happened() {
			fmt.Fprintf(io.Out, "  The upstream moved from %s at %s (base-URL configuration change).\n", st.UpstreamMoved.From, st.UpstreamMoved.At)
		}
	case !st.Enabled && !st.Injected():
		fmt.Fprintln(io.Out, "  Not enabled. Run `trajector enable` to contribute from this project.")
	default:
		fmt.Fprintln(io.Out, "  WARNING: the injected settings and the routing table disagree. Run `trajector doctor`.")
	}

	fmt.Fprintln(io.Out, "\nProxy")
	switch d.Proxy.Holder {
	case proxylife.HolderOurs:
		h := d.Proxy.Health
		up := time.Duration(h.UptimeSeconds) * time.Second
		fmt.Fprintf(io.Out, "  Running at %s: version %s, up %s.\n", d.Proxy.Addr, h.Version, up)
		// These counters live in the running proxy's memory, so they
		// begin at the uptime printed on the line above — not at
		// midnight. The proxy restarts often enough (idle exit, version
		// handover, reboot) that calling them a day's work made the
		// number read low, in the one direction a user reads as "it is
		// not recording". They are named for what they actually count.
		fmt.Fprintf(io.Out, "  Recorded since it started: %d (SSE degraded: %d, dropped: %d).\n",
			h.RecordedToday, h.SSEDegradedToday, h.CapturesDropped)
		if n := len(h.RecentRecordingErrors); n > 0 {
			fmt.Fprintf(io.Out, "  Recent recording errors: %d (last: %s)\n", n, h.RecentRecordingErrors[n-1])
		}
	case proxylife.HolderForeign:
		fmt.Fprintf(io.Out, "  WARNING: %v.\n", d.Proxy.Reason)
		if remedy := ProxyRemedy(d.Proxy.Reason); remedy != "" {
			fmt.Fprintf(io.Out, "  %s\n", remedy)
		}
	default:
		fmt.Fprintln(io.Out, "  Not running; it starts on demand with the next session.")
	}

	fmt.Fprintln(io.Out, "\nSpool")
	switch {
	case d.Spool.OpenErr != nil:
		fmt.Fprintf(io.Out, "  WARNING: %s.\n", spoolUnusableHeadline(m.deps.Layout.SpoolDir(), d.Spool.OpenErr))
		fmt.Fprintln(io.Out, "  Run `trajector doctor`.")
	case d.Spool.WritableErr != nil:
		fmt.Fprintf(io.Out, "  %s of %s used.\n", platform.HumanBytes(d.Spool.Usage), platform.HumanBytes(d.Spool.Quota))
		fmt.Fprintf(io.Out, "  WARNING: %s.\n", spoolUnwritableHeadline(d.Spool.WritableErr))
		if d.Spool.Full() {
			fmt.Fprintf(io.Out, "  %s\n", spoolFullRemedy)
		} else {
			fmt.Fprintln(io.Out, "  Run `trajector doctor`.")
		}
	default:
		fmt.Fprintf(io.Out, "  %s of %s used.\n", platform.HumanBytes(d.Spool.Usage), platform.HumanBytes(d.Spool.Quota))
	}

	fmt.Fprintln(io.Out, "\nUploads")
	if r := d.Uploads.LastUpload; r != nil {
		fmt.Fprintf(io.Out, "  Last upload: %d rawcall(s) (%s) at %s.\n",
			r.Records, platform.HumanBytes(r.Bytes), r.At.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintln(io.Out, "  Never uploaded.")
	}
	if d.Uploads.LastError != "" {
		fmt.Fprintf(io.Out, "  Last error: %s (%s).\n", d.Uploads.LastError, d.Uploads.LastErrorAt.UTC().Format(time.RFC3339))
	}
	switch {
	case d.RejectedErr != nil:
		fmt.Fprintf(io.Out, "  WARNING: %s.\n", rejectedUnreadableHeadline(m.deps.Layout.RejectedDir(), d.RejectedErr))
		fmt.Fprintln(io.Out, "  Run `trajector doctor`.")
	case len(d.Rejected) > 0:
		fmt.Fprintf(io.Out, "  WARNING: %s.\n", quarantineHeadline(d.Rejected))
		fmt.Fprintln(io.Out, "  Run `trajector doctor` to inspect them, then requeue or discard them.")
	}

	// A minimum this build already meets is not news: it arrives on every
	// acknowledgement, and reporting it would leave a compliant machine
	// permanently told to upgrade. See standing.
	version := standing(d.Handshake.MinClientVersion, m.deps.Version)
	// The service's own words about a refusal are cleared the moment it
	// acknowledges an upload, so their presence is a live refusal and is
	// reported whatever the comparison says.
	if version != versionSatisfied || d.Handshake.Notice != "" || d.UpgradeMessage != "" || d.Authorization.Required {
		fmt.Fprintln(io.Out, "\nService")
		if version != versionSatisfied {
			fmt.Fprintf(io.Out, "  The service requires client version %s or newer; this build is %s.\n",
				d.Handshake.MinClientVersion, m.deps.Version)
		}
		// The service's own words come before the instruction: they may
		// say why, or by when, and a user who reads only the first line
		// should read the reason rather than the remedy.
		if d.UpgradeMessage != "" {
			fmt.Fprintf(io.Out, "  "+serviceSays+"\n", d.UpgradeMessage)
		}
		// Only a build known to be behind is told to upgrade. On an
		// unorderable pair the requirement is stated and the remedy is
		// not: upgrade has nothing to install for a dev build, and would
		// send the user somewhere that tells them so.
		if version == versionBehind || d.UpgradeMessage != "" {
			fmt.Fprintf(io.Out, "  %s\n", upgradeHint)
		}
		if d.Authorization.Required {
			fmt.Fprintln(io.Out, "  "+authorizationPaused)
			if d.Authorization.Message != "" {
				fmt.Fprintf(io.Out, "  "+serviceSays+"\n", d.Authorization.Message)
			}
			fmt.Fprintf(io.Out, "  %s\n", authorizeHint(d.Authorization.URL))
		}
		if d.Handshake.Notice != "" {
			fmt.Fprintf(io.Out, "  Notice from the service: %s\n", d.Handshake.Notice)
		}
	}
	return nil
}
