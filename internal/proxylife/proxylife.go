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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
// re-derives it from the health payload.
type Holder int

const (
	// HolderNone: nothing accepted the connection.
	HolderNone Holder = iota
	// HolderForeign: something answered, but not as a trajector proxy.
	// Injected credentials must never be routed at it.
	HolderForeign
	// HolderOurs: a trajector proxy answered and Health carries its
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

// Ensure makes sure a healthy proxy of this version is listening:
// already-healthy is a no-op, a stale version is asked to drain and
// replaced, nothing listening is started. Concurrent callers converge
// because the port bind is the single-instance lock and losers defer to
// the winner.
func (p *Proxy) Ensure() error {
	switch h, holder := p.Health(); holder {
	case HolderForeign:
		return fmt.Errorf("%w: %s", ErrPortOccupied, p.addr)
	case HolderOurs:
		if h.Version == p.version {
			return nil
		}
		// A proxy from another binary version holds the port. Left
		// alone it could live until its next idle exit, so ask it to
		// drain and take over.
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
	conn, err := net.DialTimeout("tcp", p.addr, probeTimeout)
	if err != nil {
		return Health{}, HolderNone
	}
	conn.Close()

	var h Health
	if err := p.get(apiproxy.HealthzPath, &h); err != nil || h.Service != apiproxy.ServiceName {
		return Health{}, HolderForeign
	}
	return h, HolderOurs
}

// Selfcheck asks the proxy what it would do with token, over the exact
// injected base-URL shape and without producing an upstream call.
func (p *Proxy) Selfcheck(token string) (Selfcheck, error) {
	var reply Selfcheck
	if err := p.getURL(p.BaseURL(token)+apiproxy.SelfcheckPath, &reply); err != nil {
		return Selfcheck{}, err
	}
	return reply, nil
}

// Stop asks a running proxy to drain and exit. Nothing listening is
// already the goal state, so Stop is idempotent and never fails for it.
func (p *Proxy) Stop() {
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Post("http://"+p.addr+apiproxy.DrainPath, "", nil)
	if err == nil {
		resp.Body.Close()
	}
}

func (p *Proxy) get(path string, into any) error {
	return p.getURL("http://"+p.addr+path, into)
}

func (p *Proxy) getURL(rawURL string, into any) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(rawURL)
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
		h, holder := p.Health()
		if holder == HolderOurs && h.Version == p.version {
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
func (p *Proxy) Flush(force bool) (upload.FlushReply, error) {
	flushURL := "http://" + p.addr + upload.FlushPath
	if force {
		flushURL += "?" + url.Values{"force": {"1"}}.Encode()
	}
	client := &http.Client{Timeout: flushTimeout}
	resp, err := client.Post(flushURL, "", nil)
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
