// Package proxylife is the capture proxy as seen from outside the
// process that serves it: starting one when traffic needs it, asking
// it about itself, telling it to flush or stop. It is the only place
// that knows how to invoke this binary as a proxy, so no caller has to
// spell the argv, the endpoints, or the takeover dance itself.
// Assembling the served proxy is the composition root's job, not this
// package's.
package proxylife

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/upload"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// The proxy is reached through this binary, under one fixed argv. Every
// construction and every parse of it lives in this file.
const (
	// Command is the subcommand that hosts the proxy.
	Command = "proxy"
	// Supervise runs the watchdog that keeps a proxy child alive.
	Supervise = "run"
	// Serve runs the proxy itself.
	Serve = "serve"

	addrFlag = "--addr"
	idleFlag = "--idle-timeout"
)

// Addr is the address a production proxy listens on; apiproxy.Addr
// explains why the port is fixed.
const Addr = apiproxy.Addr

// ErrPortOccupied reports that the proxy's port is held by something
// that is not a trajector proxy. Spawning would fight the foreign
// process for the port and injected credentials would be sent to it, so
// callers must surface this loudly instead of retrying.
var ErrPortOccupied = errors.New("port occupied by a process that is not the trajector proxy")

// ErrProxyUnverified reports a port holder that engages the admin-token
// challenge without proving it knows a token published on this machine.
// A missing or stale publication produces exactly this while the holder
// is the user's own proxy, so surfaces present an authentication
// problem, never advice to hunt the process down. The holder stays
// untrusted either way: no credential is routed at it.
var ErrProxyUnverified = errors.New("could not verify the proxy")

// Timeouts for the lazy lifecycle.
const (
	probeTimeout = 500 * time.Millisecond
	// adminTimeout bounds one management exchange end to end, so a
	// holder that accepts a connection and then answers nothing costs a
	// command one wait rather than hanging it.
	adminTimeout = 5 * time.Second
	startTimeout = 10 * time.Second
	drainTimeout = 20 * time.Second
	// foreignSettle is how long a bound port that cannot yet prove
	// itself is given to turn out to be a proxy still coming up, before
	// it is called foreign. It only has to outlast the gap between the
	// bind and the published admin token.
	//
	// The two budgets answer different questions and are deliberately
	// left unaligned: adminTimeout bounds one exchange, foreignSettle
	// bounds the retrying of a holder that answers but cannot prove
	// itself yet. Because one exchange outlasts the whole retry window,
	// a holder that answers nothing is probed once and never retried;
	// the two compose additively at worst, when an attempt starts just
	// inside the window and then wedges.
	foreignSettle = 2 * time.Second
)

// flushTimeout bounds one requested flush as seen by the CLI. A drain
// of a long offline backlog uploads many batches in one call.
const flushTimeout = 10 * time.Minute

// Health is the proxy's self-report. The proxy that writes it and the
// callers that read it share this type, so the wire contract is checked
// by the compiler.
type Health = apiproxy.Health

// Holder names what, if anything, holds the proxy port. The
// discrimination lives here — misreading it touches the invariant that
// credentials are only ever routed at our own proxy, so no caller
// re-derives it from the health payload. A holder is ours only after it
// answers a challenge that proves it knows this user's published admin
// token; what it says about itself is never the verdict.
type Holder int

const (
	// HolderNone: nothing accepted the connection.
	HolderNone Holder = iota
	// HolderForeign: something answered, but could not prove it is this
	// user's trajector proxy. Injected credentials must never be routed
	// at it and the admin token must never be sent to it.
	HolderForeign
	// HolderOurs: a trajector proxy proved itself and Health carries its
	// self-report.
	HolderOurs
)

// String names the holder for diagnostics.
func (h Holder) String() string {
	switch h {
	case HolderForeign:
		return "foreign"
	case HolderOurs:
		return "ours"
	default:
		return "none"
	}
}

// Verdict is one reading of the proxy port: who holds it, what a
// holder proven ours says about itself, and why an unproven one could
// not be trusted. It carries no credential, so a surface may render or
// serialize it whole. Whether this build may take the port over is not
// a field to re-derive but the two predicates below.
type Verdict struct {
	// Addr is the address this verdict is about.
	Addr string
	// Holder is the proven answer to who holds the port.
	Holder Holder
	// Health is the holder's self-report, zero unless Holder is
	// HolderOurs.
	Health Health
	// Reason explains a HolderForeign verdict: which way the holder's
	// proof failed — no proof offered, unverifiable for want of a
	// matching admin token, silent, or unintelligible. It is what
	// separates an authentication problem from a genuine stranger, so
	// surfaces render it instead of reaching a verdict of their own. It
	// is non-nil for every HolderForeign verdict and nil otherwise.
	Reason error
}

// Replaceable reports that a proxy of ours holds the port and this
// build may take it over: only a strictly older release is replaced,
// which is the one spelling of that decision — see ReuseReason for the
// sentence surfaces print and supersedes for the ordering rule.
func (v Verdict) Replaceable(ourVersion string) bool {
	return v.Holder == HolderOurs && supersedes(ourVersion, v.Health.Version)
}

// Serving reports that a proxy of ours holds the port and this build
// leaves it alone: an equal, newer, or unordered version keeps
// serving. Ensure is a no-op against such a holder, and a proxy just
// started is up once this is true of it.
func (v Verdict) Serving(ourVersion string) bool {
	return v.Holder == HolderOurs && !v.Replaceable(ourVersion)
}

// Selfcheck is what the proxy reports about one project token.
type Selfcheck = apiproxy.Selfcheck

// Proxy is the capture proxy as seen from outside the process that runs
// it: something to ensure is up, ask about, and stop.
type Proxy struct {
	layout   userdirs.Layout
	version  string
	execPath string
	addr     string
}

// For describes the proxy this binary would start on this machine.
func For(layout userdirs.Layout, version, execPath, addr string) *Proxy {
	if addr == "" {
		addr = apiproxy.Addr
	}
	return &Proxy{layout: layout, version: version, execPath: execPath, addr: addr}
}

// Addr is where the proxy listens.
func (p *Proxy) Addr() string { return p.addr }

// BaseURL is the base URL injected into a project enabled with token.
func (p *Proxy) BaseURL(token string) string { return "http://" + p.addr + "/t/" + token }

// Ensure makes sure a healthy proxy is listening: a holder this build
// leaves serving is a no-op, a replaceable one is asked to drain and
// taken over, and nothing listening is started. A differing-version
// reuse is noted in the proxy log. Concurrent callers converge because
// the port bind is the single-instance lock and losers defer to the
// winner.
func (p *Proxy) Ensure() error {
	v := p.Settled()
	switch {
	case v.Holder == HolderForeign:
		return v.Reason
	case v.Serving(p.version):
		if v.Health.Version != p.version {
			p.noteReuse(v.Health.Version)
		}
		return nil
	case v.Replaceable(p.version):
		// A strictly older release holds the port. Left alone it could
		// live until its next idle exit, so ask it to drain and take
		// over. A drain that already failed names what stands in the
		// way; waiting out the port would bury that cause under a
		// timeout that cannot succeed.
		if err := p.Stop(); err != nil {
			return fmt.Errorf("asking the previous proxy to drain: %w", err)
		}
		if err := p.waitPortFree(drainTimeout); err != nil {
			return err
		}
	}

	argv := []string{Command, Supervise, addrFlag, p.addr}
	if _, err := startDetached(p.execPath, argv, p.layout.ProxyLog()); err != nil {
		return fmt.Errorf("starting proxy: %w", err)
	}
	return p.waitHealthy()
}

// Observe reads the port as it stands and pays no startup grace: it is
// what report-only surfaces call, so a diagnosis answers at once. A
// holder that only proves itself a moment from now is reported as it
// is right now; callers about to act on the verdict want Settled
// instead, and the method name is the whole difference.
func (p *Proxy) Observe() Verdict {
	token, holder, why := p.verify()
	if holder != HolderOurs {
		return Verdict{Addr: p.addr, Holder: holder, Reason: why}
	}
	var h Health
	if err := p.get(apiproxy.HealthzPath, token, &h, "a health request"); err != nil {
		return Verdict{Addr: p.addr, Holder: HolderForeign, Reason: err}
	}
	if h.Service != apiproxy.ServiceName {
		return Verdict{Addr: p.addr, Holder: HolderForeign, Reason: fmt.Errorf("proxy at %s calls itself %q", p.addr, h.Service)}
	}
	return Verdict{Addr: p.addr, Holder: HolderOurs, Health: h}
}

// verify establishes who holds the port before anything is trusted or
// sent to it, and reports the token the holder proved it knows. The
// holder must answer a fresh nonce with proof that it knows a token
// published for this address: a health payload can be copied by any
// listener, the proof cannot. The challenge request itself carries no
// credential, so probing a holder that turns out to be foreign leaks
// nothing to it. A holder that is not proven ours comes back
// HolderForeign with the reason the proof failed; the reason never
// carries a token.
func (p *Proxy) verify() (string, Holder, error) {
	conn, err := net.DialTimeout("tcp", p.addr, probeTimeout)
	if err != nil {
		return "", HolderNone, nil
	}
	conn.Close()

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", HolderForeign, fmt.Errorf("probing %s: %v", p.addr, err)
	}
	challenge := hex.EncodeToString(nonce[:])

	req, err := http.NewRequest(http.MethodGet, "http://"+p.addr+apiproxy.HealthzPath, nil)
	if err != nil {
		return "", HolderForeign, fmt.Errorf("probing %s: %v", p.addr, err)
	}
	req.Header.Set(apiproxy.ChallengeHeader, challenge)
	resp, err := adminClient(adminTimeout).Do(req)
	if err != nil {
		return "", HolderForeign, fmt.Errorf("the holder of %s did not answer a probe: %v", p.addr, transportCause(err))
	}
	resp.Body.Close()
	proof := resp.Header.Get(apiproxy.ProofHeader)
	if proof == "" {
		return "", HolderForeign, fmt.Errorf("%w: %s", ErrPortOccupied, p.addr)
	}

	// The published tokens are read after the answer arrives: a proxy
	// publishes before it serves its first request, so a sibling that
	// just won the bind is never judged against a pre-publish read.
	// Every candidate is tried — a crashed predecessor's leftover or an
	// older proxy's fixed-name publication may sit beside the live one —
	// and a candidate that proves nothing is skipped, never trusted. No
	// match after an answered request means no proxy of ours is serving
	// here.
	readable := 0
	for _, path := range p.layout.AdminTokenCandidates(p.addr) {
		token, err := fsatomic.ReadFile(path)
		if err != nil || len(token) == 0 {
			continue
		}
		readable++
		if hmac.Equal([]byte(proof), []byte(apiproxy.Proof(string(token), challenge, p.addr))) {
			return string(token), HolderOurs, nil
		}
	}
	if readable == 0 {
		return "", HolderForeign, fmt.Errorf("%w at %s: it answered the admin-token challenge, but no admin token for this address could be read", ErrProxyUnverified, p.addr)
	}
	return "", HolderForeign, fmt.Errorf("%w at %s: its challenge answer matches none of the admin tokens published for this address", ErrProxyUnverified, p.addr)
}

// settledVerify is verify with the startup window Settled explains
// allowed for: the management calls that act on the holder go through
// it, so a sibling that has bound the port but not published yet is
// waited out instead of being blamed for the port.
func (p *Proxy) settledVerify() (string, Holder, error) {
	return settle(p.verify)
}

// settle retries a HolderForeign verdict until the startup grace runs
// out. Every caller about to act on a verdict reads it through here;
// nothing else does.
func settle[T any](verdict func() (T, Holder, error)) (T, Holder, error) {
	deadline := time.Now().Add(foreignSettle)
	for {
		v, holder, why := verdict()
		if holder != HolderForeign || time.Now().After(deadline) {
			return v, holder, why
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Settled is Observe with the startup window allowed for, and the name
// says what it costs: up to foreignSettle of waiting. A proxy between
// binding its port and publishing its admin token is indistinguishable
// from a stranger holding the port, and a sibling that won the bind a
// moment ago is by far the likelier of the two: concurrent starts
// converge only because losers defer to the winner, which a loser
// reporting ErrPortOccupied does not do. Only callers about to act on
// the verdict pay it — Ensure, a serve process that just lost the
// bind, the wait for a freshly started proxy, and the drain and flush
// requests aimed at the holder. Report-only surfaces call Observe.
func (p *Proxy) Settled() Verdict {
	v, _, _ := settle(func() (Verdict, Holder, error) {
		v := p.Observe()
		return v, v.Holder, v.Reason
	})
	return v
}

// Selfcheck asks the proxy what it would do with token, over the exact
// injected base-URL shape and without producing an upstream call. The
// request carries no admin credential: it exercises what an injected
// client would send, nothing more.
func (p *Proxy) Selfcheck(token string) (Selfcheck, error) {
	req, err := http.NewRequest(http.MethodGet, p.BaseURL(token)+apiproxy.SelfcheckPath, nil)
	if err != nil {
		return Selfcheck{}, err
	}
	var reply Selfcheck
	if err := p.doJSON(req, &reply, "a self-check request", adminTimeout); err != nil {
		return Selfcheck{}, err
	}
	return reply, nil
}

// Stop asks a running proxy to drain and exit, and reports why no
// drain was delivered or accepted. Nothing listening is already the
// goal state, so that is a nil return. The drain request carries the
// admin token, so it goes only to a holder that proved it knows that
// token; an unproven holder is not Stop's to fight — its verdict's
// reason comes back for the caller to surface.
func (p *Proxy) Stop() error {
	token, holder, why := p.settledVerify()
	switch holder {
	case HolderNone:
		return nil
	case HolderForeign:
		return why
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+p.addr+apiproxy.DrainPath, nil)
	if err != nil {
		return err
	}
	authorize(req, token)
	resp, err := adminClient(adminTimeout).Do(req)
	if err != nil {
		return fmt.Errorf("proxy at %s: %w", p.addr, transportCause(err))
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return p.answered(resp, "a drain request")
	}
	return nil
}

// authorize attaches the admin token the holder proved it knows.
// Every caller runs behind a successful verify: the raw token must
// never probe a holder that has not proven it already knows it.
func authorize(req *http.Request, token string) {
	req.Header.Set(apiproxy.AdminHeader, token)
}

func (p *Proxy) get(path, token string, into any, what string) error {
	req, err := http.NewRequest(http.MethodGet, "http://"+p.addr+path, nil)
	if err != nil {
		return err
	}
	authorize(req, token)
	return p.doJSON(req, into, what, adminTimeout)
}

// doJSON performs one management request and decodes its answer. Every
// failure names the proxy, so a truncated or malformed body never
// surfaces as a bare decoding error far from its cause.
func (p *Proxy) doJSON(req *http.Request, into any, what string, timeout time.Duration) error {
	resp, err := adminClient(timeout).Do(req)
	if err != nil {
		return fmt.Errorf("proxy at %s: %w", p.addr, transportCause(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p.answered(resp, what)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into); err != nil {
		return fmt.Errorf("proxy at %s sent an unreadable answer to %s: %w", p.addr, what, err)
	}
	return nil
}

// answered turns an unexpected status into an error naming the proxy.
// A 401 means the admin token this CLI sent was not the serving one —
// an authentication failure, never a stranger.
func (p *Proxy) answered(resp *http.Response, what string) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w at %s: it answered 401 Unauthorized to %s", ErrProxyUnverified, p.addr, what)
	}
	return fmt.Errorf("proxy at %s answered %s to %s", p.addr, resp.Status, what)
}

// adminClient is the one construction of the client a management
// request rides; the timeout bounds the whole exchange. The client
// carries a connection pool of its own, and keeps no connection alive
// past the exchange: on the process-wide pool a connection opened
// before the port changed hands outlives the proxy that answered on
// it, and the next management request — a drain or a flush, which no
// transport may replay — is handed that dead connection and fails with
// an EOF that says nothing about the proxy now listening.
func adminClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	return &http.Client{Timeout: timeout, Transport: transport}
}

// transportCause strips the URL a *url.Error embeds — on the selfcheck
// path it carries the project token, which must never reach an error
// string a caller prints — and keeps the cause.
func transportCause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// noteReuse records that a proxy of another version was left serving.
// The line goes to the proxy log, where the serving proxy's own output
// already lands, so coexisting builds leave one combined record of who
// deferred to whom. Best-effort by design: failing to write the line
// never changes the decision.
func (p *Proxy) noteReuse(holder string) {
	f, err := openLogAppend(p.layout.ProxyLog())
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s this build (%s) reuses the version %s proxy at %s: %s\n",
		time.Now().UTC().Format(time.RFC3339), p.version, holder, p.addr, ReuseReason)
}

func (p *Proxy) waitPortFree(within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		conn, err := net.DialTimeout("tcp", p.addr, 100*time.Millisecond)
		if err != nil {
			return nil
		}
		conn.Close()
		if time.Now().After(deadline) {
			return fmt.Errorf("previous proxy at %s did not release the port within %s", p.addr, within)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (p *Proxy) waitHealthy() error {
	deadline := time.Now().Add(startTimeout)
	for {
		// The bind may have been won by a sibling rather than this
		// call's own spawn; any holder Ensure would reuse counts as
		// healthy, or a loser would wait out a winner it defers to.
		v := p.Settled()
		if v.Serving(p.version) {
			return nil
		}
		if time.Now().After(deadline) {
			lastProbe := ""
			if v.Reason != nil {
				lastProbe = fmt.Sprintf(" (last probe: %v)", v.Reason)
			}
			return fmt.Errorf("proxy did not become healthy at %s within %s%s (log: %s)", p.addr, startTimeout, lastProbe, p.layout.ProxyLog())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Supervise runs the watchdog process: it keeps a proxy child alive and
// ends with the child's clean idle exit.
func (p *Proxy) Supervise(ctx context.Context, idle time.Duration, stdout, stderr io.Writer) error {
	argv := []string{p.execPath, Command, Serve, addrFlag, p.addr}
	if idle > 0 {
		argv = append(argv, idleFlag, idle.String())
	}
	return superviseChild(ctx, superviseConfig{
		Command: argv,
		Stdout:  stdout,
		Stderr:  stderr,
		Logf: func(format string, a ...any) {
			fmt.Fprintf(stderr, format+"\n", a...)
		},
	})
}

// Flush asks a running proxy to upload now and reports what it did.
// The flush request carries the admin token, so it goes only to a
// holder that proved it knows that token.
func (p *Proxy) Flush(force bool) (upload.FlushReply, error) {
	token, holder, why := p.settledVerify()
	switch holder {
	case HolderNone:
		return upload.FlushReply{}, fmt.Errorf("no proxy is listening at %s", p.addr)
	case HolderForeign:
		return upload.FlushReply{}, why
	}
	flushURL := "http://" + p.addr + upload.FlushPath
	if force {
		flushURL += "?" + url.Values{"force": {"1"}}.Encode()
	}
	req, err := http.NewRequest(http.MethodPost, flushURL, nil)
	if err != nil {
		return upload.FlushReply{}, err
	}
	authorize(req, token)
	var reply upload.FlushReply
	if err := p.doJSON(req, &reply, "a flush request", flushTimeout); err != nil {
		return upload.FlushReply{}, err
	}
	return reply, nil
}
