// Package apiproxy is the local capture proxy. It forwards API traffic
// transparently and records eligible calls as a best-effort sidecar.
// Forwarding is sacred: no failure anywhere on the recording path may
// interrupt or alter what the client and upstream exchange.
package apiproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// httpURL reports whether s can serve as a forwarding or classification
// origin.
func httpURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// Addr is the production listen address. The port is fixed: injected
// project settings embed it, so a fallback port would strand every
// enabled project. Binding is loopback-only; the proxy must never be
// reachable from outside the machine.
const Addr = "127.0.0.1:41100"

// Reserved path prefix for the proxy's own endpoints; requests under
// it are never forwarded upstream.
const internalPrefix = "/trajector/"

// HealthzPath serves proxy identity and counters.
const HealthzPath = "/trajector/healthz"

// DrainPath asks the proxy to drain in-flight requests and exit; a
// newer binary uses it to take over the port.
const DrainPath = "/trajector/drain"

// SelfcheckPath, requested under a token prefix (/t/<token> +
// SelfcheckPath), reports whether that token would be routed and
// recorded. It exercises the exact injected base URL shape without
// producing an upstream call.
const SelfcheckPath = "/trajector/selfcheck"

// Health is the proxy's self-report: who it is and what its recording
// has been doing. Lifecycle probes read Service and Version to tell this
// proxy apart from a foreign process squatting the port.
type Health struct {
	Service               string   `json:"service"`
	Version               string   `json:"version"`
	UptimeSeconds         int      `json:"uptime_seconds"`
	RecordedToday         int      `json:"recorded_today"`
	SSEDegradedToday      int      `json:"sse_degraded_today"`
	CapturesDropped       int      `json:"captures_dropped"`
	UnusableRouteUpstream int      `json:"unusable_route_upstream"`
	RecentRecordingErrors []string `json:"recent_recording_errors"`
}

// Selfcheck is what the proxy reports about one token. Both the proxy
// that writes it and the CLI that reads it use this type, so the wire
// contract is checked by the compiler rather than by two hand-written
// decoders agreeing.
type Selfcheck struct {
	Service    string `json:"service"`
	Version    string `json:"version"`
	TokenKnown bool   `json:"token_known"`
	Recording  bool   `json:"recording"`
	// Decision and PauseReason carry the routing verdict verbatim, so a
	// surface above the proxy can explain why recording is off instead
	// of only reporting that it is.
	Decision       string `json:"decision"`
	PauseReason    string `json:"pause_reason,omitempty"`
	ProjectIDHash  string `json:"project_id_hash"`
	UpstreamOrigin string `json:"upstream_origin"`
	SpoolWritable  bool   `json:"spool_writable"`
}

// ServiceName identifies this proxy in healthz responses so lifecycle
// probes can tell it apart from a foreign process squatting the port.
const ServiceName = "trajector-proxy"

// Defaults for the lazy lifecycle.
const (
	defaultIdleTimeout  = 30 * time.Minute
	defaultDrainTimeout = 15 * time.Second
)

// Config wires the proxy's collaborators.
type Config struct {
	Version string
	Table   *routing.Table
	// Dialect is the provider profile this proxy captures. Its
	// OfficialUpstream is the origin-classification oracle; it is a
	// separate fact from DefaultUpstream, so pointing the fallback
	// somewhere else can never silently relabel what gets recorded.
	Dialect capture.Dialect
	// DefaultUpstream receives traffic that carries no valid consent
	// token. Such traffic is forwarded untouched and never recorded.
	DefaultUpstream string
	Spool           *spool.Spool
	// IdleTimeout is how long authorized traffic may be silent before
	// the proxy exits on its own. Zero selects a default.
	IdleTimeout time.Duration
	// DrainTimeout bounds how long shutdown waits for in-flight
	// requests. Zero selects a default.
	DrainTimeout time.Duration
	// MaxRecordBytes bounds how much of one exchange the recorder holds
	// in memory. Zero selects DefaultMaxRecordBytes.
	MaxRecordBytes int64
	Logf           func(format string, args ...any)
	// Internal serves the composition root's own endpoints under the
	// reserved prefix, after the proxy's built-ins. It is not part of
	// the capture path: a mounted call may legitimately run for minutes
	// (a flush of a long backlog), so it never counts as inflight and
	// cannot hold the drain/idle machinery open. Nil answers not-found.
	Internal http.Handler
}

// Server is one proxy instance.
type Server struct {
	cfg     Config
	handler http.Handler
	start   time.Time

	stats stats

	mu             sync.Mutex
	lastAuthorized time.Time
	inflight       int

	// records carries finished captures to the one goroutine allowed to
	// touch the disk, so no request goroutine ever waits on a write.
	records     chan func(context.Context)
	recordsDone sync.WaitGroup
	queueMu     sync.RWMutex
	queueClosed bool

	drainOnce sync.Once
	drainCh   chan struct{}
}

// New validates the configuration and builds a server.
func New(cfg Config) (*Server, error) {
	if cfg.Table == nil || cfg.Spool == nil {
		return nil, fmt.Errorf("apiproxy: routing table and spool are required")
	}
	if cfg.Dialect.Provider == "" || cfg.Dialect.ShouldRecord == nil {
		return nil, fmt.Errorf("apiproxy: a capture dialect is required")
	}
	if !httpURL(cfg.DefaultUpstream) {
		return nil, fmt.Errorf("apiproxy: default upstream %q is not an http(s) URL", cfg.DefaultUpstream)
	}
	if !httpURL(cfg.Dialect.OfficialUpstream) {
		return nil, fmt.Errorf("apiproxy: official upstream %q is not an http(s) URL", cfg.Dialect.OfficialUpstream)
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	if cfg.DrainTimeout == 0 {
		cfg.DrainTimeout = defaultDrainTimeout
	}
	if cfg.MaxRecordBytes == 0 {
		cfg.MaxRecordBytes = DefaultMaxRecordBytes
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	s := &Server{
		cfg:     cfg,
		start:   time.Now(),
		records: make(chan func(context.Context), recordQueueDepth),
		drainCh: make(chan struct{}),
	}
	s.lastAuthorized = s.start
	s.recordsDone.Add(1)
	go s.runRecordQueue()
	forward := s.newForwarder()
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, internalPrefix) && r.URL.Path != HealthzPath && r.URL.Path != DrainPath {
			// Mounted endpoints run outside the inflight accounting: the
			// drain/idle machinery exists for captures, never for them.
			if s.cfg.Internal != nil {
				s.cfg.Internal.ServeHTTP(w, r)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		s.trackInflight(func() {
			if strings.HasPrefix(r.URL.Path, internalPrefix) {
				s.serveInternal(w, r)
				return
			}
			// The proxy's own endpoints are reserved even under a token
			// prefix: they must never leak upstream as ordinary paths.
			if token, rest, ok := splitToken(r.URL.Path); ok && strings.HasPrefix(rest, internalPrefix) {
				s.serveTokenInternal(w, r, token, rest)
				return
			}
			forward.ServeHTTP(w, r)
		})
	})
	return s, nil
}

// Serve runs the proxy on l until the context is canceled, a drain is
// requested, or authorized traffic has been idle past the timeout. A
// drained or idle exit returns nil: it is the normal end of life.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	httpSrv := &http.Server{Handler: s.handler}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		tick := s.cfg.IdleTimeout / 8
		if tick < 5*time.Millisecond {
			tick = 5 * time.Millisecond
		}
		if tick > 5*time.Second {
			tick = 5 * time.Second
		}
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
			case <-s.drainCh:
			case <-ticker.C:
				if !s.idle() {
					continue
				}
			}
			drainCtx, cancel := context.WithTimeout(context.Background(), s.cfg.DrainTimeout)
			defer cancel()
			if err := httpSrv.Shutdown(drainCtx); err != nil {
				httpSrv.Close()
			}
			return
		}
	}()

	err := httpSrv.Serve(l)
	<-shutdownDone
	// Captures queued by requests that already finished get their own
	// bounded window; none of it held the exchanges open.
	s.closeRecordQueue(s.cfg.DrainTimeout)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight == 0 && time.Since(s.lastAuthorized) >= s.cfg.IdleTimeout
}

func (s *Server) trackInflight(serve func()) {
	s.mu.Lock()
	s.inflight++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inflight--
		s.mu.Unlock()
	}()
	serve()
}

func (s *Server) touchAuthorized() {
	s.mu.Lock()
	s.lastAuthorized = time.Now()
	s.mu.Unlock()
}

func (s *Server) serveInternal(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == HealthzPath && r.Method == http.MethodGet:
		s.serveHealthz(w)
	case r.URL.Path == DrainPath && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusAccepted)
		s.drainOnce.Do(func() { close(s.drainCh) })
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveTokenInternal(w http.ResponseWriter, r *http.Request, token, rest string) {
	if rest != SelfcheckPath || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	route, verdict := s.cfg.Table.Lookup(token)
	upstream := s.cfg.DefaultUpstream
	if verdict.Resolves() {
		upstream = route.Upstream
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Selfcheck{
		Service:        ServiceName,
		Version:        s.cfg.Version,
		TokenKnown:     verdict.Resolves(),
		Recording:      verdict.Records(),
		Decision:       string(verdict.Decision),
		PauseReason:    verdict.PauseReason,
		ProjectIDHash:  route.ProjectIDHash,
		UpstreamOrigin: envelope.Origin(upstream, s.cfg.Dialect.OfficialUpstream),
		SpoolWritable:  s.cfg.Spool.Writable() == nil,
	})
}

// Health reports the proxy's identity and counters: the same facts the
// healthz endpoint serves, for in-process callers such as the
// composition root's batch run metadata.
func (s *Server) Health() Health {
	h := s.stats.snapshot()
	h.Service = ServiceName
	h.Version = s.cfg.Version
	h.UptimeSeconds = int(time.Since(s.start) / time.Second)
	return h
}

func (s *Server) serveHealthz(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.Health())
}

// stats counts recording outcomes. Counters feed healthz and, later,
// batch run metadata; they never influence forwarding.
type stats struct {
	mu               sync.Mutex
	day              string
	recordedToday    int
	degradedToday    int
	dropped          int
	unusableUpstream int
	recentErrors     []string
}

func (st *stats) roll(now time.Time) {
	day := now.UTC().Format("20060102")
	if st.day != day {
		st.day = day
		st.recordedToday = 0
		st.degradedToday = 0
	}
}

func (st *stats) recorded(now time.Time, degraded bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.roll(now)
	st.recordedToday++
	if degraded {
		st.degradedToday++
	}
}

// countDropped records a capture the guarded region gave up on.
func (st *stats) countDropped() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.dropped++
}

// countUnusableUpstream records a route whose upstream could not be
// parsed, which is forwarded at the default upstream and never recorded.
func (st *stats) countUnusableUpstream() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.unusableUpstream++
}

func (st *stats) recordError(msg string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.recentErrors = append(st.recentErrors, msg)
	if len(st.recentErrors) > 5 {
		st.recentErrors = st.recentErrors[len(st.recentErrors)-5:]
	}
}

func (st *stats) snapshot() Health {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.roll(time.Now())
	return Health{
		RecordedToday:         st.recordedToday,
		SSEDegradedToday:      st.degradedToday,
		CapturesDropped:       st.dropped,
		UnusableRouteUpstream: st.unusableUpstream,
		RecentRecordingErrors: append([]string(nil), st.recentErrors...),
	}
}
