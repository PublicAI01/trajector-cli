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
// proxy just to look at one.
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
		fmt.Fprintf(io.Out, "  Recorded today: %d (SSE degraded: %d, dropped: %d).\n",
			h.RecordedToday, h.SSEDegradedToday, h.CapturesDropped)
		if n := len(h.RecentRecordingErrors); n > 0 {
			fmt.Fprintf(io.Out, "  Recent recording errors: %d (last: %s)\n", n, h.RecentRecordingErrors[n-1])
		}
	case proxylife.HolderForeign:
		fmt.Fprintf(io.Out, "  WARNING: %s is held by a process that is not the trajector proxy. %s\n", d.Proxy.Addr, PortOccupiedRemedy)
	default:
		fmt.Fprintln(io.Out, "  Not running; it starts on demand with the next session.")
	}

	if d.Spool.OpenErr != nil {
		return d.Spool.OpenErr
	}
	fmt.Fprintln(io.Out, "\nSpool")
	fmt.Fprintf(io.Out, "  %s of %s used.\n", platform.HumanBytes(d.Spool.Usage), platform.HumanBytes(d.Spool.Quota))
	if d.Spool.Full() {
		fmt.Fprintln(io.Out, "  WARNING: the spool is full; recording is stopped until space frees. Run `trajector upload --force`.")
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
	if len(d.Rejected) > 0 {
		fmt.Fprintf(io.Out, "  WARNING: %s.\n", quarantineHeadline(d.Rejected))
		fmt.Fprintln(io.Out, "  Run `trajector doctor` to inspect and requeue them.")
	}

	if d.Handshake.MinClientVersion != "" || d.Handshake.Notice != "" {
		fmt.Fprintln(io.Out, "\nService")
		if d.Handshake.MinClientVersion != "" {
			fmt.Fprintf(io.Out, "  The service requires client version %s or newer; this build is %s.\n",
				d.Handshake.MinClientVersion, m.deps.Version)
		}
		if d.Handshake.Notice != "" {
			fmt.Fprintf(io.Out, "  Notice from the service: %s\n", d.Handshake.Notice)
		}
	}
	return nil
}
