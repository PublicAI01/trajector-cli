// Package proxylife owns the capture proxy's life: starting it when
// traffic needs one, assembling and running it, asking it to stop, and
// answering what it is currently doing. It is the only place that knows
// how to invoke this binary as a proxy, so no caller has to spell the
// argv, the endpoints, or the takeover dance itself.
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
	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
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

// Addr is the address a production proxy listens on. The port is fixed:
// injected project settings embed it, so a fallback port would strand
// every enabled project.
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

// flushInterval is how often a served proxy checks the upload
// thresholds.
const flushInterval = time.Minute

// flushTimeout bounds one requested flush as seen by the CLI. A drain
// of a long offline backlog uploads many batches in one call.
const flushTimeout = 10 * time.Minute

// Health is the proxy's self-report. The proxy that writes it and the
// callers that read it share this type, so the wire contract is checked
// by the compiler.
type Health = apiproxy.Health

// Selfcheck is what the proxy reports about one project token.
type Selfcheck = apiproxy.Selfcheck

// Proxy is the capture proxy as seen from outside the process that runs
// it: something to ensure is up, ask about, and stop.
type Proxy struct {
	layout   userdirs.Layout
	version  string
	execPath string
	addr     string

	service *platform.Client
	tokens  tokenstore.Store
}

// For describes the proxy this binary would start on this machine.
func For(layout userdirs.Layout, version, execPath, addr string) *Proxy {
	if addr == "" {
		addr = apiproxy.Addr
	}
	return &Proxy{layout: layout, version: version, execPath: execPath, addr: addr}
}

// Uploads arranges for a proxy served by Run to host the uploader,
// draining the spool to service against the device token in tokens.
// Without it a served proxy only captures.
func (p *Proxy) Uploads(service *platform.Client, tokens tokenstore.Store) {
	p.service = service
	p.tokens = tokens
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
	if h, running := p.Health(); running {
		if h.Service != apiproxy.ServiceName {
			return fmt.Errorf("%w: %s", ErrPortOccupied, p.addr)
		}
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

// Health reports the proxy's identity and counters. running is false
// when nothing accepted the connection; it is true with a zero Health
// when something answered but not as a trajector proxy.
func (p *Proxy) Health() (Health, bool) {
	conn, err := net.DialTimeout("tcp", p.addr, probeTimeout)
	if err != nil {
		return Health{}, false
	}
	conn.Close()

	var h Health
	if err := p.get(apiproxy.HealthzPath, &h); err != nil {
		return Health{}, true
	}
	return h, true
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
		h, running := p.Health()
		if running && h.Service == apiproxy.ServiceName && h.Version == p.version {
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

// Run assembles and serves the proxy itself. Losing the port bind to a
// healthy proxy is the normal concurrent-start outcome and exits
// quietly; losing it to anything else is a loud failure — never a
// fallback to another port, which would strand every injected base URL.
func (p *Proxy) Run(ctx context.Context, idle time.Duration, stdout, stderr io.Writer) error {
	logf := func(format string, a ...any) {
		fmt.Fprintf(stderr, format+"\n", a...)
	}
	// The spool quota is whatever the service last said in the upload
	// handshake; a machine that never uploaded runs on the default.
	sp, err := spool.Create(p.layout.SpoolDir(), upload.LoadHandshake(p.layout.UploadDir()).SpoolQuotaBytes)
	if err != nil {
		return fmt.Errorf("opening spool: %w", err)
	}

	// The server's flush hook and the uploader's run metadata refer to
	// each other; the uploader is assembled second and published through
	// this variable before the server starts serving.
	var uploader *upload.Uploader
	cfg := apiproxy.Config{
		Version:         p.version,
		Table:           routing.New(p.layout.RoutingTable(), 0),
		DefaultUpstream: capture.OfficialUpstream,
		Spool:           sp,
		IdleTimeout:     idle,
		Logf:            logf,
	}
	if p.service != nil {
		cfg.Flush = func(force bool) (upload.Result, error) {
			return uploader.Flush(force)
		}
	}
	server, err := apiproxy.New(cfg)
	if err != nil {
		return err
	}
	if p.service != nil {
		uploader, err = upload.New(upload.Deps{
			Spool:       sp,
			Service:     p.service,
			DeviceToken: p.deviceToken,
			Version:     p.version,
			Dir:         p.layout.UploadDir(),
			RejectedDir: p.layout.RejectedDir(),
			Run:         server.RunMetadata,
			Logf:        logf,
		})
		if err != nil {
			return err
		}
	}

	l, err := net.Listen("tcp", p.addr)
	if err != nil {
		if h, running := p.Health(); running && h.Service == apiproxy.ServiceName {
			fmt.Fprintf(stdout, "proxy already running at %s (version %s)\n", p.addr, h.Version)
			return nil
		}
		return fmt.Errorf("%w: %s", ErrPortOccupied, p.addr)
	}

	var served chan struct{}
	if uploader != nil {
		served = make(chan struct{})
		go periodicFlush(ctx, served, uploader, logf)
	}
	err = server.Serve(ctx, l)
	if uploader != nil {
		close(served)
		// One last threshold check on the way out: the proxy is the only
		// resident process, so anything it leaves unflushed waits for the
		// next session.
		if _, ferr := uploader.Flush(false); ferr != nil {
			logf("final flush: %v", ferr)
		}
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

// deviceToken reads the device pairing token for the uploader. A
// missing token is the signed-out state, reported as empty so uploads
// pause rather than fail.
func (p *Proxy) deviceToken() (string, error) {
	secret, err := p.tokens.Load(tokenstore.DeviceTokenName)
	if errors.Is(err, tokenstore.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(secret), nil
}

// Flush asks a running proxy to upload now and reports what it did.
func (p *Proxy) Flush(force bool) (apiproxy.FlushReply, error) {
	flushURL := "http://" + p.addr + apiproxy.FlushPath
	if force {
		flushURL += "?" + url.Values{"force": {"1"}}.Encode()
	}
	client := &http.Client{Timeout: flushTimeout}
	resp, err := client.Post(flushURL, "", nil)
	if err != nil {
		return apiproxy.FlushReply{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiproxy.FlushReply{}, fmt.Errorf("proxy at %s answered %s to a flush request", p.addr, resp.Status)
	}
	var reply apiproxy.FlushReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply); err != nil {
		return apiproxy.FlushReply{}, err
	}
	return reply, nil
}
