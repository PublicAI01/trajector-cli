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
	"net/http"
	"sync/atomic"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/sessionread"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/upload"
	"github.com/PublicAI01/trajector-cli/internal/userconfig"
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
// and nothing else can travel along with it. Every store a command also
// holds arrives here already open, and Serve opens none of them again;
// the spool is the one store Serve opens, through OpenSpool.
type Assembly struct {
	Layout userdirs.Layout
	// Tokens holds the device token every upload authenticates with.
	Tokens *tokenstore.Store
	// Service is the client uploads and handshakes go through.
	Service *platform.Client
	// Consent is the durable record of withdrawal the uploader consults
	// before it sends a project's rawcalls.
	Consent *consent.Store
	// Routes is the routing table: where a pause is written, which
	// grants the session files are read for, and the source of the
	// cached view the proxy looks every token up in.
	Routes *routing.Store
	// Registry holds which session files each enabled project reads and
	// how far each is read.
	Registry *follow.Registry
	// Version is this build's identity, announced on the proxy's health
	// endpoint and reported to the service on every upload.
	Version string
	// ExecPath is this binary, which the watchdog spawns the serving
	// child from.
	ExecPath string
	// Addr is the loopback address the proxy listens on.
	Addr string
	// Home is the user's home directory, which reading holds a field
	// naming a path against.
	Home string
}

func (a Assembly) proxy() *proxylife.Proxy {
	return proxylife.For(a.Layout, a.Version, a.ExecPath, a.Addr, nil)
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
	layout := a.Layout
	// The stop signal ends serving, not uploading: a batch in flight
	// finishes within its own budget, and the exit flush runs after the
	// signal.
	uploads := context.WithoutCancel(ctx)
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
		// A record this build cannot mask must not leave the device,
		// and neither may the ones behind it: recording pauses under
		// routing.PauseRedactionDrift, which states why. The routing table
		// is the one place a pause is written, and the proxy re-reads
		// it on its next lookup.
		OnUnmaskableRecord: func() {
			if err := a.Routes.PauseByBuild(routing.PauseRedactionDrift, a.Version); err != nil {
				logf("upload: warning: a record could not be masked and recording could not be paused: %v", err)
			}
		},
		// Read for every batch rather than once at start, so turning
		// image and document upload off takes effect without restarting
		// the proxy. A file that cannot be read turns it off, as
		// userconfig states; the failure is logged, and status states it.
		OmitImagesAndDocuments: func() bool {
			cfg, err := userconfig.Read(layout.ConfigFile())
			if err != nil {
				logf("upload: warning: %v; %s", err, userconfig.UnreadableConsequence)
			}
			return cfg.OmitsImagesAndDocuments()
		},
	})
	if err != nil {
		return err
	}

	// The served proxy reads session files too: one file when a hook
	// reports it gained lines, and the hot set on a cadence. It shares
	// the spool, the routing store, and the uploader with the capture
	// path, so what a read stores is flushed by the one flusher.
	tail := &tailer{
		reader: sessionread.Reader{
			Registry:  a.Registry,
			Routes:    a.Routes,
			Spool:     sp,
			Version:   a.Version,
			ReaderLog: layout.ReaderLog(),
			Home:      a.Home,
			Now:       time.Now,
		},
		routes: a.Routes,
		alive:  follow.ProcessAlive,
		nudge:  newFlushNudge(),
	}
	internal := http.NewServeMux()
	internal.Handle(upload.FlushPath, uploader.Handler(apiproxy.ServiceName))
	internal.Handle("POST "+apiproxy.ProgressPath, tail.handler())

	server, err = apiproxy.New(apiproxy.Config{
		Version:         a.Version,
		Table:           a.Routes.Table(0),
		Dialect:         capture.Anthropic,
		DefaultUpstream: capture.Anthropic.OfficialUpstream,
		Spool:           sp,
		IdleTimeout:     idle,
		Liveness:        tail.liveness,
		Logf:            logf,
		Internal:        internal,
		AdminTokens:     layout,
		// One last flush on the way out, past the byte and age
		// thresholds and behind every gate, run while this process still
		// holds the listen port: the bind is what excludes the next
		// proxy's flusher, so no upload of this process may continue past
		// its release, or the two would drain the same spool records
		// under different batch ids.
		BeforeShutdown: func() {
			if ferr := uploader.Close(uploads, exitFlushBudget); ferr != nil {
				logf("final flush: %v", ferr)
			}
		},
	})
	if err != nil {
		return err
	}

	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", a.Addr)
	if err != nil {
		// Losing the bind says nothing about who won it: a sibling that
		// won a moment ago may not have published its admin token yet, so
		// this verdict gets the same settled grace Ensure acts on. And
		// when nothing is listening at all, the port was never contested —
		// the bind failed for its own reason (permissions, a broken
		// network stack) and that error is the report, not an occupancy
		// verdict. A stop signal inside the grace must not end the probes
		// early: a cancelled probe reads as nothing listening.
		switch v := a.proxy().Settled(context.WithoutCancel(ctx)); v.Holder {
		case proxylife.HolderOurs:
			fmt.Fprintf(stdout, "proxy already running at %s (version %s)\n", a.Addr, v.Health.Version)
			return nil
		case proxylife.HolderForeign:
			return v.Reason
		default:
			return fmt.Errorf("binding %s: %w", a.Addr, err)
		}
	}

	flush := func(atOnce bool) error {
		if atOnce {
			_, err := uploader.FlushRecords(uploads)
			return err
		}
		_, err := uploader.Flush(uploads, false)
		return err
	}
	served := make(chan struct{})
	go periodicFlush(ctx, served, tail.nudge, flush, logf)
	go periodicSweep(ctx, served, tail.sweep, flush, logf)
	err = server.Serve(ctx, l)
	close(served)
	return err
}

// flushNudge is how a reader that may not upload asks the flush
// cadence to upload for it. It holds one pending ask: two asks
// between two flushes are one flush, and an ask for every record at
// once outlives an ask for a threshold check, because the session
// that made it has no later ask coming.
type flushNudge struct {
	signal chan struct{}
	atOnce atomic.Bool
}

func newFlushNudge() *flushNudge {
	return &flushNudge{signal: make(chan struct{}, 1)}
}

// ask never blocks and never fails: the caller is answering a hook
// that a session waits on.
func (n *flushNudge) ask(atOnce bool) {
	if atOnce {
		n.atOnce.Store(true)
	}
	select {
	case n.signal <- struct{}{}:
	default:
	}
}

// takeAtOnce reports whether the flush now starting must send every
// record, and clears the ask.
func (n *flushNudge) takeAtOnce() bool { return n.atOnce.Swap(false) }

// periodicFlush checks the upload thresholds every flushInterval while the
// proxy serves, and on the nudges the reading of session files leaves
// for it. Failures are logged and retried on the next tick; the spool
// keeps everything until a batch is acknowledged.
func periodicFlush(ctx context.Context, served <-chan struct{}, nudge *flushNudge, flush func(atOnce bool) error, logf func(string, ...any)) {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		atOnce := false
		select {
		case <-ctx.Done():
			return
		case <-served:
			return
		case <-ticker.C:
		case <-nudge.signal:
			// A nudge the cadence never reached is dropped when the
			// proxy ends: the exit flush sends what it asked for.
			if ctx.Err() != nil {
				return
			}
			atOnce = nudge.takeAtOnce()
		}
		if err := flush(atOnce); err != nil {
			if errors.Is(err, upload.ErrClosed) {
				// The exit flush already ran; the cadence is done.
				return
			}
			logf("flush: %v", err)
		}
	}
}

// periodicSweep looks at the hot session files every sweepInterval while
// the proxy serves, and once on the way in: a proxy that starts after
// a session did meets the file the session's earlier hooks left hot.
// It runs on a goroutine of its own, which no hook waits on, so the
// flush a sweep makes due is taken here and not handed to the flush
// cadence.
//
// A sweep that read nothing flushes nothing: the flush cadence owns
// the thresholds, and nothing new is waiting on them. What a sweep
// cooled or read leaves at once rather than on the thresholds for
// records read from session files: a file the sweep found grown is a
// file whose hook never reported it, so its lines already missed the
// read that would have sent them on time, and a threshold of their
// own would only hold them longer.
//
// The sweep on the way in is the exception for what it read: a proxy
// that has just bound the port must not open an upload before it can
// be drained — the takeover of its port waits on the exit flush, which
// waits on that upload's lock — so what it read gets one threshold
// check instead.
func periodicSweep(ctx context.Context, served <-chan struct{}, sweep func(onStart bool) (read, cooled bool), flush func(atOnce bool) error, logf func(string, ...any)) {
	sweepAndFlush := func(onStart bool) {
		read, cooled := sweep(onStart)
		if !read && !cooled {
			return
		}
		if err := flush(cooled || !onStart); err != nil {
			logf("flush: %v", err)
		}
	}
	sweepAndFlush(true)
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-served:
			return
		case <-ticker.C:
			sweepAndFlush(false)
		}
	}
}
