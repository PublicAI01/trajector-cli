// Package proxyserve assembles the capture proxy a process serves: the
// spool, the routing table, and the resident uploader that is this
// process's one flusher, wired together here and nowhere else. It is
// the serving process's composition root, distinct from the lifecycle
// machine that assembles a command's stores — a command never builds an
// uploader, and a serving proxy never builds a project's consent — so
// the two are named apart rather than sharing one struct.
package proxyserve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/redact"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/upload"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// flushInterval is how often a served proxy checks the upload
// thresholds.
const flushInterval = time.Minute

// exitFlushBudget bounds the flush a serving proxy runs on its way out.
// That flush holds the listen port, which is what keeps a successor's
// flusher out, and a successor that asked this proxy to drain waits
// only so long for the port before reporting it was never released. A
// flush on a slow link can run far longer than that — one attempt alone
// may take half an hour by the regular proportional budget — so the
// exit path is capped well inside the successor's wait, leaving room
// for the listener close behind it. What the cap cuts off is not lost:
// those records stay in the spool, and whoever flushes next, this
// proxy's successor included, picks them up under the batch id already
// pinned for them.
const exitFlushBudget = 15 * time.Second

// Assembly is everything a served proxy is built from. It is stated as
// values and stores rather than as the machine that happens to hold
// them: what the serving process needs is then visible in one struct,
// and nothing else can travel along with it.
type Assembly struct {
	Layout userdirs.Layout
	// Tokens holds the device token every upload authenticates with.
	Tokens *tokenstore.Store
	// Service is the client uploads and handshakes go through.
	Service *platform.Client
	// Consent is the durable record of withdrawal the uploader consults
	// before it sends a project's rawcalls.
	Consent *consent.Store
	// Version is this build's identity, announced on the proxy's health
	// endpoint and reported to the service on every upload.
	Version string
	// ExecPath is this binary, which the watchdog spawns the serving
	// child from.
	ExecPath string
	// Addr is the loopback address the proxy listens on.
	Addr string
}

func (a Assembly) proxy() *proxylife.Proxy {
	return proxylife.For(a.Layout, a.Version, a.ExecPath, a.Addr)
}

// OpenSpool opens the capture spool with the quota the service last set
// through the upload handshake; a machine that never uploaded runs on
// the default. The serving proxy writes into it and the read-only
// surfaces report on it, so "what is the quota" is answered this one
// way.
func OpenSpool(layout userdirs.Layout) (*spool.Spool, error) {
	return spool.Create(layout.SpoolDir(), upload.LoadHandshake(layout.UploadDir()).SpoolQuotaBytes)
}

// Supervise runs the watchdog process: it keeps a proxy child alive and
// ends with the child's clean idle exit.
func Supervise(ctx context.Context, a Assembly, idle time.Duration, stdout, stderr io.Writer) error {
	return a.proxy().Supervise(ctx, idle, stdout, stderr)
}

// Serve assembles and serves the capture proxy in this process. Losing
// the port bind to a healthy proxy is the normal concurrent-start
// outcome and exits quietly; losing it to anything else is a loud
// failure — never a fallback to another port, which would strand every
// injected base URL.
func Serve(ctx context.Context, a Assembly, idle time.Duration, stdout, stderr io.Writer) error {
	// The bind is the invariant: enabled projects route API credentials
	// at this address, so it must name this machine and nothing else.
	// Checked here as well as where the address was resolved, because
	// this is the line that actually makes the proxy reachable — every
	// other caller only passes the address along. 2026-08-15.
	if err := apiproxy.ValidateAddr(a.Addr); err != nil {
		return err
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(stderr, format+"\n", args...)
	}
	// The serve process is the machine's one flusher, so this is the one
	// place the redaction pass is configured. Email and phone patterns
	// are the personally identifying strings PRIVACY.md promises to mask;
	// broader patterns (street addresses) misfire too often on code and
	// prose to be safe against observed values.
	redact.ConfigurePII(redact.PIIEmail, redact.PIIPhone)
	layout := a.Layout
	sp, err := OpenSpool(layout)
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
		Service: a.Service,
		DeviceToken: func() (string, error) {
			token, _, err := a.Tokens.DeviceToken()
			return token, err
		},
		// The consent store is the durable record of withdrawal, and it
		// outlives both the proxy's cached routing verdict and the
		// short-lived process that ran `disable`; see dropWithdrawn. A
		// store that cannot be read is not a withdrawal.
		Withdrawn: func(projectIDHash string) bool {
			state, ok, err := a.Consent.ProjectState(projectIDHash)
			return err == nil && ok && state == consent.StateDenied
		},
		Version:     a.Version,
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
		Version:         a.Version,
		Table:           routing.New(layout.RoutingTable(), 0),
		Dialect:         capture.Anthropic,
		DefaultUpstream: capture.Anthropic.OfficialUpstream,
		Spool:           sp,
		IdleTimeout:     idle,
		Logf:            logf,
		Internal:        uploader.Handler(apiproxy.ServiceName),
		AdminTokens:     layout,
		// One last threshold check on the way out, run while this process
		// still holds the listen port: the bind is what excludes the next
		// proxy's flusher, so no upload of this process may continue past
		// its release, or the two would drain the same spool records
		// under different batch ids.
		BeforeShutdown: func() {
			if ferr := uploader.Close(exitFlushBudget); ferr != nil {
				logf("final flush: %v", ferr)
			}
		},
	})
	if err != nil {
		return err
	}

	l, err := net.Listen("tcp", a.Addr)
	if err != nil {
		// Losing the bind says nothing about who won it: a sibling that
		// won a moment ago may not have published its admin token yet, so
		// this verdict gets the same settled grace Ensure acts on. And
		// when nothing is listening at all, the port was never contested —
		// the bind failed for its own reason (permissions, a broken
		// network stack) and that error is the report, not an occupancy
		// verdict.
		switch v := a.proxy().Settled(); v.Holder {
		case proxylife.HolderOurs:
			fmt.Fprintf(stdout, "proxy already running at %s (version %s)\n", a.Addr, v.Health.Version)
			return nil
		case proxylife.HolderForeign:
			return v.Reason
		default:
			return fmt.Errorf("binding %s: %w", a.Addr, err)
		}
	}

	served := make(chan struct{})
	go periodicFlush(ctx, served, uploader, logf)
	err = server.Serve(ctx, l)
	close(served)
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
				if errors.Is(err, upload.ErrClosed) {
					// The exit flush already ran; the cadence is done.
					return
				}
				logf("flush: %v", err)
			}
		}
	}
}
