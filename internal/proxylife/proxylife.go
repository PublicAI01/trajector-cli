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
	"os"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
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

// Timeouts for the lazy lifecycle.
const (
	probeTimeout = 500 * time.Millisecond
	startTimeout = 10 * time.Second
	drainTimeout = 20 * time.Second
	// foreignSettle is how long a bound port that cannot yet prove
	// itself is given to turn out to be a proxy still coming up, before
	// it is called foreign. It only has to outlast the gap between the
	// bind and the published admin token.
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

// Ensure makes sure a healthy proxy is listening: already-healthy is a
// no-op, a strictly older release is asked to drain and replaced, and
// nothing listening is started. A holder whose version is equal, newer,
// or out of order with this build's — see Supersedes — is reused as it
// stands, and a differing-version reuse is noted in the proxy log.
// Concurrent callers converge because the port bind is the
// single-instance lock and losers defer to the winner.
func (p *Proxy) Ensure() error {
	switch h, holder := p.settledHealth(); holder {
	case HolderForeign:
		return fmt.Errorf("%w: %s", ErrPortOccupied, p.addr)
	case HolderOurs:
		if !Supersedes(p.version, h.Version) {
			if h.Version != p.version {
				p.noteReuse(h.Version)
			}
			return nil
		}
		// A strictly older release holds the port. Left alone it could
		// live until its next idle exit, so ask it to drain and take
		// over.
		p.Stop()
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

// Health reports who holds the proxy port and, when it is ours, the
// proxy's self-report. Health is zero unless the holder is HolderOurs.
func (p *Proxy) Health() (Health, Holder) {
	if holder := p.verify(); holder != HolderOurs {
		return Health{}, holder
	}
	var h Health
	if err := p.get(apiproxy.HealthzPath, &h); err != nil || h.Service != apiproxy.ServiceName {
		return Health{}, HolderForeign
	}
	return h, HolderOurs
}

// verify establishes who holds the port before anything is trusted or
// sent to it. The holder must answer a fresh nonce with proof that it
// knows the admin token published for this layout: a health payload can
// be copied by any listener, the proof cannot. The challenge request
// itself carries no credential, so probing a holder that turns out to
// be foreign leaks nothing to it.
func (p *Proxy) verify() Holder {
	conn, err := net.DialTimeout("tcp", p.addr, probeTimeout)
	if err != nil {
		return HolderNone
	}
	conn.Close()

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return HolderForeign
	}
	challenge := hex.EncodeToString(nonce[:])

	req, err := http.NewRequest(http.MethodGet, "http://"+p.addr+apiproxy.HealthzPath, nil)
	if err != nil {
		return HolderForeign
	}
	req.Header.Set(apiproxy.ChallengeHeader, challenge)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return HolderForeign
	}
	resp.Body.Close()
	proof := resp.Header.Get(apiproxy.ProofHeader)
	if proof == "" {
		return HolderForeign
	}

	// The published token is read after the answer arrives: a proxy
	// publishes before it serves its first request, so a sibling that
	// just won the bind is never judged against a pre-publish read. No
	// token on disk after an answered request means no proxy of ours is
	// serving here.
	token, err := os.ReadFile(p.layout.AdminTokenFile())
	if err != nil || len(token) == 0 {
		return HolderForeign
	}
	if !hmac.Equal([]byte(proof), []byte(apiproxy.Proof(string(token), challenge, p.addr))) {
		return HolderForeign
	}
	return HolderOurs
}

// settledHealth is Health with the startup window allowed for. A proxy
// between binding its port and answering its first request is
// indistinguishable from a stranger holding the port, and the caller
// that won the bind a moment ago is by far the likelier of the two:
// Ensure's whole convergence story is that losers defer to the winner,
// which a loser reporting ErrPortOccupied does not do. Only Ensure needs
// this — it acts on the verdict, where Health's other callers report it.
func (p *Proxy) settledHealth() (Health, Holder) {
	deadline := time.Now().Add(foreignSettle)
	for {
		h, holder := p.Health()
		if holder != HolderForeign || time.Now().After(deadline) {
			return h, holder
		}
		time.Sleep(50 * time.Millisecond)
	}
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
	if err := p.doJSON(req, &reply); err != nil {
		return Selfcheck{}, err
	}
	return reply, nil
}

// Stop asks a running proxy to drain and exit. Nothing listening is
// already the goal state, so Stop is idempotent and never fails for it.
// The drain request carries the admin token, so it goes only to a
// holder that proved it knows that token; an unproven holder is not
// Stop's to fight — Ensure and the diagnosis surfaces shout about it.
func (p *Proxy) Stop() {
	if p.verify() != HolderOurs {
		return
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+p.addr+apiproxy.DrainPath, nil)
	if err != nil {
		return
	}
	p.authorize(req)
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// authorize attaches the admin token a serving proxy published for its
// reserved endpoints. Every caller runs behind a successful verify: the
// raw token must never probe a holder that has not proven it already
// knows it. An unreadable token file just means the request goes out
// bare and the proxy answers 401: the missing-file case is
// indistinguishable from no proxy running, and both surface through
// the request's own failure.
func (p *Proxy) authorize(req *http.Request) {
	data, err := os.ReadFile(p.layout.AdminTokenFile())
	if err != nil {
		return
	}
	req.Header.Set(apiproxy.AdminHeader, string(data))
}

func (p *Proxy) get(path string, into any) error {
	req, err := http.NewRequest(http.MethodGet, "http://"+p.addr+path, nil)
	if err != nil {
		return err
	}
	p.authorize(req)
	return p.doJSON(req, into)
}

func (p *Proxy) doJSON(req *http.Request, into any) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// A transport failure surfaces as a *url.Error whose message embeds
		// the requested URL — which on the selfcheck path carries the
		// project token. Report the cause without the URL so the token
		// cannot reach an error string a caller prints.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("proxy at %s: %w", p.addr, ue.Err)
		}
		return fmt.Errorf("proxy at %s: %w", p.addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("proxy at %s answered %s", p.addr, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into)
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
		h, holder := p.Health()
		if holder == HolderOurs && !Supersedes(p.version, h.Version) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("proxy did not become healthy at %s within %s (log: %s)", p.addr, startTimeout, p.layout.ProxyLog())
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
	switch p.verify() {
	case HolderNone:
		return upload.FlushReply{}, fmt.Errorf("no proxy is listening at %s", p.addr)
	case HolderForeign:
		return upload.FlushReply{}, fmt.Errorf("%w: %s", ErrPortOccupied, p.addr)
	}
	flushURL := "http://" + p.addr + upload.FlushPath
	if force {
		flushURL += "?" + url.Values{"force": {"1"}}.Encode()
	}
	req, err := http.NewRequest(http.MethodPost, flushURL, nil)
	if err != nil {
		return upload.FlushReply{}, err
	}
	p.authorize(req)
	client := &http.Client{Timeout: flushTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return upload.FlushReply{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return upload.FlushReply{}, fmt.Errorf("proxy at %s answered %s to a flush request", p.addr, resp.Status)
	}
	var reply upload.FlushReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply); err != nil {
		return upload.FlushReply{}, err
	}
	return reply, nil
}
