// Package proxytest runs a real capture proxy against an isolated
// sandbox: its own routing table, spool, and fake upstream. Tests drive
// it through the same HTTP surface a client would, and read back what it
// stored through the same spool a batch run would.
package proxytest

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// settle is how long a poll waits before giving up on something the
// proxy does off the request path.
const settle = 5 * time.Second

// Health is what the proxy reports about itself. Like Selfcheck below,
// the type is the proxy's own: the harness reads the wire contract, it
// does not redeclare it.
type Health = apiproxy.Health

type options struct {
	layout     userdirs.Layout
	haveLayout bool
	addr       string
	version    string
	official   string
	quota      int64
	idle       time.Duration
	drain      time.Duration
	maxRecord  int64
	logf       func(format string, args ...any)
}

// Option configures one sandbox.
type Option func(*options)

// WithLayout puts the proxy's routing table and spool where layout says,
// so a test can drive the CLI and the proxy against the same files.
func WithLayout(l userdirs.Layout) Option {
	return func(o *options) { o.layout, o.haveLayout = l, true }
}

// WithAddr pins the listen address instead of taking a free one.
func WithAddr(addr string) Option { return func(o *options) { o.addr = addr } }

// WithVersion sets the version the proxy reports as its own.
func WithVersion(v string) Option { return func(o *options) { o.version = v } }

// WithOfficialUpstream declares what counts as the provider's own
// origin, independently of where unrouted traffic is forwarded. The
// default is the sandbox upstream, so recorded exchanges classify as
// official unless a test says otherwise.
func WithOfficialUpstream(url string) Option {
	return func(o *options) { o.official = url }
}

// WithFullSpool starts the proxy with a spool that has no room left, so
// every capture fails on the write.
func WithFullSpool() Option { return func(o *options) { o.quota = 1 } }

// WithIdleTimeout sets how long authorized traffic may be silent before
// the proxy exits on its own.
func WithIdleTimeout(d time.Duration) Option { return func(o *options) { o.idle = d } }

// WithMaxRecordBytes caps how much of one exchange the recorder holds.
func WithMaxRecordBytes(n int64) Option { return func(o *options) { o.maxRecord = n } }

// WithLogf routes the proxy's log lines to f. It doubles as the fault
// injector for the guarded region: a panicking f is the only way an
// outside caller can make recording itself blow up.
func WithLogf(f func(format string, args ...any)) Option { return func(o *options) { o.logf = f } }

// Env is one running proxy and the sandbox around it.
type Env struct {
	t *testing.T
	// Upstream is the default upstream this proxy forwards to. Nothing
	// in a sandbox can reach the real API.
	Upstream *fakeupstream.Server

	layout   userdirs.Layout
	addr     string
	spool    *spool.Spool
	stopped  chan struct{}
	serveErr error
	cancel   context.CancelFunc
}

// New starts a proxy and stops it with the test.
func New(t *testing.T, opts ...Option) *Env {
	t.Helper()
	o := options{version: "1.2.3", idle: time.Hour, drain: time.Second}
	for _, apply := range opts {
		apply(&o)
	}
	if !o.haveLayout {
		o.layout = SandboxLayout(t, t.TempDir())
	}

	e := &Env{
		t:        t,
		Upstream: fakeupstream.New(t),
		layout:   o.layout,
		stopped:  make(chan struct{}),
	}
	sp, err := spool.Create(o.layout.SpoolDir(), o.quota)
	if err != nil {
		t.Fatal(err)
	}
	// Reading back what the proxy stored must not share the writer's
	// quota bookkeeping, so readers open the same directory separately.
	e.spool, err = spool.Open(o.layout.SpoolDir(), 0)
	if err != nil {
		t.Fatal(err)
	}

	dialect := capture.Anthropic
	dialect.OfficialUpstream = e.Upstream.URL()
	if o.official != "" {
		dialect.OfficialUpstream = o.official
	}
	server, err := apiproxy.New(apiproxy.Config{
		Version:         o.version,
		Table:           routing.New(o.layout.RoutingTable(), time.Millisecond),
		Dialect:         dialect,
		DefaultUpstream: e.Upstream.URL(),
		Spool:           sp,
		IdleTimeout:     o.idle,
		DrainTimeout:    o.drain,
		MaxRecordBytes:  o.maxRecord,
		Logf:            o.logf,
	})
	if err != nil {
		t.Fatal(err)
	}

	listenOn := o.addr
	if listenOn == "" {
		listenOn = "127.0.0.1:0"
	}
	l, err := net.Listen("tcp", listenOn)
	if err != nil {
		t.Fatal(err)
	}
	e.addr = l.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go func() {
		e.serveErr = server.Serve(ctx, l)
		close(e.stopped)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-e.stopped:
		case <-time.After(10 * time.Second):
			t.Error("proxy did not shut down")
		}
	})
	return e
}

// Addr is the proxy's listen address.
func (e *Env) Addr() string { return e.addr }

// BaseURL is the proxy's origin.
func (e *Env) BaseURL() string { return "http://" + e.addr }

// TokenURL is the base URL injected into a project enabled with token.
func (e *Env) TokenURL(token string) string { return e.BaseURL() + "/t/" + token }

// Layout is where this sandbox keeps its files.
func (e *Env) Layout() userdirs.Layout { return e.layout }

// WriteTable replaces the routing table with content verbatim, so tests
// can write tables the writer would never produce.
func (e *Env) WriteTable(content string) {
	e.t.Helper()
	writeFile(e.t, e.layout.RoutingTable(), content)
}

// Post sends one request through the proxy.
func (e *Env) Post(path, body string, header http.Header) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.BaseURL()+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// Rawcalls reports everything currently in the spool.
func (e *Env) Rawcalls() []spool.Rawcall {
	e.t.Helper()
	var stored []spool.Rawcall
	if err := e.spool.Each(func(r spool.Rawcall) error {
		stored = append(stored, r)
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
	return stored
}

// WaitRawcalls waits for the spool to hold at least n rawcalls. Captures
// are written off the request path, so arrival is not immediate.
func (e *Env) WaitRawcalls(n int) []spool.Rawcall {
	e.t.Helper()
	deadline := time.Now().Add(settle)
	for {
		stored := e.Rawcalls()
		if len(stored) >= n {
			return stored
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("spool holds %d rawcalls, want %d", len(stored), n)
			return stored
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Healthz reads the proxy's self-report.
func (e *Env) Healthz() Health {
	e.t.Helper()
	resp, err := http.Get(e.BaseURL() + apiproxy.HealthzPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var h Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		e.t.Fatal(err)
	}
	return h
}

// WaitHealthz polls until the proxy reports what the test is waiting for.
func (e *Env) WaitHealthz(want func(Health) bool) Health {
	e.t.Helper()
	deadline := time.Now().Add(settle)
	for {
		h := e.Healthz()
		if want(h) {
			return h
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("healthz never reported the expected outcome: %+v", h)
			return h
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Selfcheck is what the proxy reports about one token.
type Selfcheck = apiproxy.Selfcheck

// Selfcheck asks the proxy what it would do with token, over the exact
// injected base-URL shape.
func (e *Env) Selfcheck(token string) Selfcheck {
	e.t.Helper()
	resp, err := http.Get(e.TokenURL(token) + apiproxy.SelfcheckPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("selfcheck status = %d", resp.StatusCode)
	}
	var reply Selfcheck
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		e.t.Fatal(err)
	}
	return reply
}

// WaitStopped waits for the proxy to end on its own and reports how
// Serve returned.
func (e *Env) WaitStopped(within time.Duration) error {
	e.t.Helper()
	select {
	case <-e.stopped:
		return e.serveErr
	case <-time.After(within):
		e.t.Fatal("proxy did not stop on its own")
		return nil
	}
}

// SandboxLayout resolves a layout with every trajector directory inside
// dir, so a test's files land exactly where production would put them.
func SandboxLayout(t *testing.T, dir string) userdirs.Layout {
	t.Helper()
	layout, err := userdirs.Resolve(userdirs.Env{
		GOOS: runtime.GOOS,
		Getenv: func(key string) string {
			switch key {
			case "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME":
				return dir
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return layout
}
