package lifecycle

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/redact"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// flushInterval is how often a served proxy checks the upload
// thresholds.
const flushInterval = time.Minute

// SuperviseProxy runs the watchdog process: it keeps a proxy child
// alive and ends with the child's clean idle exit.
func (m *Machine) SuperviseProxy(ctx context.Context, idle time.Duration, stdout, stderr io.Writer) error {
	return m.proxy.Supervise(ctx, idle, stdout, stderr)
}

// ServeProxy assembles and serves the capture proxy in this process:
// spool, routing table, and the resident uploader — the machine's one
// flusher — are wired together here and nowhere else. Losing the port
// bind to a healthy proxy is the normal concurrent-start outcome and
// exits quietly; losing it to anything else is a loud failure — never
// a fallback to another port, which would strand every injected base
// URL.
func (m *Machine) ServeProxy(ctx context.Context, idle time.Duration, stdout, stderr io.Writer) error {
	logf := func(format string, a ...any) {
		fmt.Fprintf(stderr, format+"\n", a...)
	}
	m.warnNonDefaultEndpoint(stderr)
	// The serve process is the machine's one flusher, so this is the one
	// place the redaction pass is configured. Email and phone patterns
	// are the personally identifying strings PRIVACY.md promises to mask;
	// broader patterns (street addresses) misfire too often on code and
	// prose to be safe against observed values.
	redact.ConfigurePII(redact.PIIEmail, redact.PIIPhone)
	layout := m.deps.Layout
	sp, err := m.spool()
	if err != nil {
		return fmt.Errorf("opening spool: %w", err)
	}

	// The uploader's run metadata reads the server's counters, and the
	// server mounts the uploader's flush endpoint. The metadata side
	// takes the deferred binding: it cannot run before the server serves
	// its first flush, by which time the variable is set.
	var server *apiproxy.Server
	uploader, err := upload.New(upload.Deps{
		Spool:   sp,
		Service: m.deps.Platform,
		DeviceToken: func() (string, error) {
			token, _, err := m.deps.Tokens.DeviceToken()
			return token, err
		},
		Version:     m.deps.Version,
		Dir:         layout.UploadDir(),
		RejectedDir: layout.RejectedDir(),
		Run: func() batch.Run {
			h := server.Health()
			return batch.Run{
				RecordedToday:    h.RecordedToday,
				SSEDegradedToday: h.SSEDegradedToday,
				CapturesDropped:  h.CapturesDropped,
				SpoolUsageBytes:  sp.Usage(),
				SpoolQuotaBytes:  sp.Quota(),
			}
		},
		Logf: logf,
	})
	if err != nil {
		return err
	}

	server, err = apiproxy.New(apiproxy.Config{
		Version:         m.deps.Version,
		Table:           routing.New(layout.RoutingTable(), 0),
		Dialect:         capture.Anthropic,
		DefaultUpstream: capture.Anthropic.OfficialUpstream,
		Spool:           sp,
		IdleTimeout:     idle,
		Logf:            logf,
		Internal:        uploader.Handler(apiproxy.ServiceName),
		AdminTokenFile:  layout.AdminTokenFile(),
	})
	if err != nil {
		return err
	}

	l, err := net.Listen("tcp", m.proxy.Addr())
	if err != nil {
		if h, holder := m.proxy.Health(); holder == proxylife.HolderOurs {
			fmt.Fprintf(stdout, "proxy already running at %s (version %s)\n", m.proxy.Addr(), h.Version)
			return nil
		}
		return fmt.Errorf("%w: %s", ErrPortOccupied, m.proxy.Addr())
	}

	served := make(chan struct{})
	go periodicFlush(ctx, served, uploader, logf)
	err = server.Serve(ctx, l)
	close(served)
	// One last threshold check on the way out: the proxy is the only
	// resident process, so anything it leaves unflushed waits for the
	// next session.
	if _, ferr := uploader.Flush(false); ferr != nil {
		logf("final flush: %v", ferr)
	}
	return err
}

// periodicFlush checks the upload thresholds on a cadence while the
// proxy serves. Failures are logged and retried on the next tick; the
// spool keeps everything until a batch is acknowledged.
func periodicFlush(ctx context.Context, served chan struct{}, uploader *upload.Uploader, logf func(string, ...any)) {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-served:
			return
		case <-ticker.C:
			if _, err := uploader.Flush(false); err != nil {
				logf("flush: %v", err)
			}
		}
	}
}
