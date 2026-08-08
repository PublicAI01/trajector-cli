// Package proxytest runs a real capture proxy against an isolated
// sandbox: its own routing table, spool, and fake upstream. Tests drive
// it through the same HTTP surface a client would, and read back what it
// stored through the same spool a batch run would.
package proxytest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
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
	internal   http.Handler
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

// WithInternal mounts handler under the proxy's reserved prefix, the
// way the composition root mounts the uploader's flush endpoint.
func WithInternal(h http.Handler) Option {
	return func(o *options) { o.internal = h }
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
	client   *http.Client
	spool    *spool.Spool
	stopped  chan struct{}
	serveErr error
	cancel   context.CancelFunc
}

// Client returns an HTTP client whose connection pool lives and dies
// with the test. On the process-wide client, pooled connections outlive
// the server they were opened to, and since test servers listen on
// ephemeral ports, a later test can be handed a pooled connection to an
// address its own server now owns but nothing is serving — an EOF with
// no relation to the test that sees it. Every request a test sends
// itself goes through a client scoped this way.
func Client(t *testing.T) *http.Client {
	t.Helper()
	client := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	t.Cleanup(client.CloseIdleConnections)
	return client
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
		client:   Client(t),
		stopped:  make(chan struct{}),
	}
	sp, err := spool.Create(o.layout.SpoolDir(), o.quota)
	if err != nil {
		t.Fatal(err)
	}
	e.spool = sp

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
		Internal:        o.internal,
		AdminTokens:     o.layout,
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
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// Do sends a caller-built request through the sandbox's own client, for
// tests that shape requests no helper would — a foreign Host, a
// hand-rolled token, a cancelable context. Errors are returned rather
// than fatal so a helper goroutine may use it too.
func (e *Env) Do(req *http.Request) (*http.Response, error) {
	return e.client.Do(req)
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

// readAdminToken is the one place a harness reads a proxy's published
// admin token off disk. An unreadable or empty candidate counts as not
// published yet: a caller that treats it as a token sends an empty one
// and gets back a 401 with no body, which surfaces as a bare decoding
// error far from the race that caused it.
func readAdminToken(layout userdirs.Layout, addr string) (string, bool) {
	for _, path := range layout.AdminTokenCandidates(addr) {
		data, err := fsatomic.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data), true
		}
	}
	return "", false
}

// plantAdminToken writes token at path the way a serving proxy would.
func plantAdminToken(t *testing.T, path, token string) {
	t.Helper()
	if err := userdirs.EnsureOwnerDir(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := fsatomic.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

// PublishAdminToken plants token where a serving proxy at addr would
// publish it, so a test can probe port holders while a real credential
// is at stake on disk. The planted instance name sorts ahead of every
// hex instance a real proxy generates, so a probe walking the
// candidates meets the planted file first and must not stop there.
func PublishAdminToken(t *testing.T, layout userdirs.Layout, addr, token string) {
	t.Helper()
	plantAdminToken(t, layout.AdminTokenFile(addr, "0"), token)
}

// RemoveAdminTokens deletes every admin-token publication for addr, so
// a test can probe a live proxy whose published tokens have gone
// missing from disk.
func RemoveAdminTokens(t *testing.T, layout userdirs.Layout, addr string) {
	t.Helper()
	for _, path := range layout.AdminTokenCandidates(addr) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

// PublishLegacyAdminToken plants token under the fixed name proxies
// published before publications became per-address, so a test can
// stand in for such a proxy.
func PublishLegacyAdminToken(t *testing.T, layout userdirs.Layout, token string) {
	t.Helper()
	plantAdminToken(t, layout.LegacyAdminTokenFile(), token)
}

// Authorize attaches the admin token published for the address the
// request targets, when one is readable. A caller probing a proxy that
// has not published yet sends the request bare and gets a 401 —
// indistinguishable from the proxy not being up, which is what its
// retry loop already handles.
func Authorize(req *http.Request, layout userdirs.Layout) {
	if token, ok := readAdminToken(layout, req.URL.Host); ok {
		req.Header.Set(apiproxy.AdminHeader, token)
	}
}

// AdminToken reads the token the served proxy published for its
// reserved endpoints, waiting for it to appear: the proxy writes it
// once it owns the port, which a fresh sandbox may not have reached
// yet.
func (e *Env) AdminToken() string {
	e.t.Helper()
	deadline := time.Now().Add(settle)
	for {
		if token, ok := readAdminToken(e.layout, e.addr); ok {
			return token
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("the proxy never published an admin token for %s", e.addr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// PostAdmin sends one POST to a reserved endpoint, carrying the admin
// token the proxy published. Post stays bare on purpose: a test that
// wants to look like a browser uses it.
func (e *Env) PostAdmin(path string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.BaseURL()+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set(apiproxy.AdminHeader, e.AdminToken())
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// readHealthz performs one authorized health read against the proxy at
// addr. A non-200 answer becomes an error carrying the status code:
// handing such a body to the JSON decoder would surface as a bare EOF
// far from the cause.
func readHealthz(client *http.Client, addr string, layout userdirs.Layout) (Health, error) {
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+apiproxy.HealthzPath, nil)
	if err != nil {
		return Health{}, err
	}
	Authorize(req, layout)
	resp, err := client.Do(req)
	if err != nil {
		return Health{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Health{}, fmt.Errorf("healthz answered status %d", resp.StatusCode)
	}
	var h Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return Health{}, fmt.Errorf("decoding healthz: %w", err)
	}
	return h, nil
}

// Healthz reads the proxy's self-report, waiting out startup.
func (e *Env) Healthz() Health {
	e.t.Helper()
	return e.WaitHealthz(func(Health) bool { return true })
}

// WaitHealthz polls until the proxy reports what the test is waiting
// for. A read that fails in transit or answers non-200 means not yet —
// a proxy mid-startup answers 401 until it publishes its admin token —
// so it keeps the poll going instead of failing the test.
func (e *Env) WaitHealthz(want func(Health) bool) Health {
	e.t.Helper()
	deadline := time.Now().Add(settle)
	for {
		h, err := readHealthz(e.client, e.addr, e.layout)
		if err == nil && want(h) {
			return h
		}
		if time.Now().After(deadline) {
			if err != nil {
				e.t.Fatalf("healthz never answered: %v", err)
			}
			e.t.Fatalf("healthz never reported the expected outcome: %+v", h)
			return h
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// WaitServing blocks until a proxy served outside this harness — the
// CLI's serve command, the lifecycle supervisor — answers healthz at
// addr with the admin token published under layout. Transient failures
// keep the poll going; the deadline failure names the last one, status
// code included.
func WaitServing(t *testing.T, client *http.Client, addr string, layout userdirs.Layout) {
	t.Helper()
	// Startup covers a whole serve assembly, not just a request the
	// proxy answers off the request path, so it outlasts settle.
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := readHealthz(client, addr, layout)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy at %s never became healthy: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Selfcheck is what the proxy reports about one token.
type Selfcheck = apiproxy.Selfcheck

// Selfcheck asks the proxy what it would do with token, over the exact
// injected base-URL shape.
func (e *Env) Selfcheck(token string) Selfcheck {
	e.t.Helper()
	resp, err := e.client.Get(e.TokenURL(token) + apiproxy.SelfcheckPath)
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
