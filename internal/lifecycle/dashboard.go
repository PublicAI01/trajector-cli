package lifecycle

import (
	"fmt"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// Status prints the device dashboard: pairing, the current project's
// consent, the proxy, the spool, uploads, and what the service last
// said. It is a report, not a judgement — it never repairs anything and
// always leaves the fixing to doctor — and it never starts a proxy just
// to look at one.
func (m *Machine) Status(dir string, io IO) error {
	fmt.Fprintf(io.Out, "trajector %s\n", m.deps.Version)

	st, err := m.Project(dir)
	if err != nil {
		return err
	}

	fmt.Fprintln(io.Out, "\nDevice")
	if m.Paired() {
		fmt.Fprintln(io.Out, "  Signed in.")
	} else {
		fmt.Fprintln(io.Out, "  Not signed in. Run `trajector login` to pair this device.")
	}
	switch st.PauseReason {
	case "":
	case pauseSignedOut:
		fmt.Fprintln(io.Out, "  Recording is paused everywhere: signed out. Run `trajector login` to resume.")
	case pauseConsent:
		fmt.Fprintln(io.Out, "  Recording is paused everywhere: the data agreement changed. Run `trajector enable` to reconfirm.")
	default:
		fmt.Fprintf(io.Out, "  Recording is paused everywhere: %s.\n", st.PauseReason)
	}

	fmt.Fprintf(io.Out, "\nProject %s\n", st.Root)
	switch {
	case st.Consistent():
		fmt.Fprintln(io.Out, "  Contributing; recording is on for this project.")
		if st.Upstream != capture.Anthropic.OfficialUpstream {
			fmt.Fprintf(io.Out, "  Upstream: %s (third-party origin).\n", st.Upstream)
		}
	case !st.Enabled && !st.Injected():
		fmt.Fprintln(io.Out, "  Not enabled. Run `trajector enable` to contribute from this project.")
	default:
		fmt.Fprintln(io.Out, "  WARNING: the injected settings and the routing table disagree. Run `trajector doctor`.")
	}

	fmt.Fprintln(io.Out, "\nProxy")
	h, running := m.proxy.Health()
	switch {
	case running && h.Service == apiproxy.ServiceName:
		up := time.Duration(h.UptimeSeconds) * time.Second
		fmt.Fprintf(io.Out, "  Running at %s: version %s, up %s.\n", m.proxy.Addr(), h.Version, up)
		fmt.Fprintf(io.Out, "  Recorded today: %d (SSE degraded: %d, dropped: %d).\n",
			h.RecordedToday, h.SSEDegradedToday, h.CapturesDropped)
		if n := len(h.RecentRecordingErrors); n > 0 {
			fmt.Fprintf(io.Out, "  Recent recording errors: %d (last: %s)\n", n, h.RecentRecordingErrors[n-1])
		}
	case running:
		fmt.Fprintf(io.Out, "  WARNING: %s is held by a process that is not the trajector proxy. Run `trajector doctor`.\n", m.proxy.Addr())
	default:
		fmt.Fprintln(io.Out, "  Not running; it starts on demand with the next session.")
	}

	handshake := upload.LoadHandshake(m.deps.Layout.UploadDir())
	sp, err := spool.Open(m.deps.Layout.SpoolDir(), handshake.SpoolQuotaBytes)
	if err != nil {
		return err
	}
	fmt.Fprintln(io.Out, "\nSpool")
	fmt.Fprintf(io.Out, "  %s of %s used.\n", humanBytes(sp.Usage()), humanBytes(sp.Quota()))
	if sp.Usage() >= sp.Quota() {
		fmt.Fprintln(io.Out, "  WARNING: the spool is full; recording is stopped until space frees. Run `trajector upload --force`.")
	}

	fmt.Fprintln(io.Out, "\nUploads")
	state := upload.LoadState(m.deps.Layout.UploadDir())
	if r := state.LastUpload; r != nil {
		fmt.Fprintf(io.Out, "  Last upload: %d rawcall(s) (%s) at %s.\n",
			r.Records, humanBytes(r.Bytes), r.At.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintln(io.Out, "  Never uploaded.")
	}
	if state.LastError != "" {
		fmt.Fprintf(io.Out, "  Last error: %s (%s).\n", state.LastError, state.LastErrorAt.UTC().Format(time.RFC3339))
	}
	rejected, err := upload.ListRejected(m.deps.Layout.RejectedDir())
	if err != nil {
		return err
	}
	if len(rejected) > 0 {
		fmt.Fprintf(io.Out, "  WARNING: %s.\n", quarantineHeadline(rejected))
		fmt.Fprintln(io.Out, "  Run `trajector doctor` to inspect and requeue them.")
	}

	if handshake.MinClientVersion != "" || handshake.Notice != "" {
		fmt.Fprintln(io.Out, "\nService")
		if handshake.MinClientVersion != "" {
			fmt.Fprintf(io.Out, "  The service requires client version %s or newer; this build is %s.\n",
				handshake.MinClientVersion, m.deps.Version)
		}
		if handshake.Notice != "" {
			fmt.Fprintf(io.Out, "  Notice from the service: %s\n", handshake.Notice)
		}
	}
	return nil
}

// humanBytes renders a byte count in binary units with one decimal.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
