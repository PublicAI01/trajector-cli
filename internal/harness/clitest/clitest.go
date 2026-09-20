// Package clitest drives the trajector CLI in-process against an
// isolated environment: a fresh HOME with all user-directory variables
// pointed inside a temp dir, so no test can read or write the
// developer's real trajector state.
package clitest

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// Env is an isolated environment for one test.
type Env struct {
	t       *testing.T
	home    string
	project string
	service *fakeplatform.Server
	client  *http.Client
	// proxyAddr is where this environment's CLI looks for its proxy.
	proxyAddr string
	// config accumulates what the user config file says, so pointing
	// the CLI at one destination does not unset another.
	config map[string]string
}

// New isolates the process environment and returns the harness. The
// user-directory variables are pinned by userdirs itself; only the
// variables the CLI reads on top of them are restated here.
func New(t *testing.T) *Env {
	t.Helper()
	home := t.TempDir()
	// The project directory is named, not left as the numeric temp-dir
	// leaf: tests assert that hashes never contain the path, and a
	// two-digit leaf matches a hex digest by chance.
	project := filepath.Join(t.TempDir(), "sample-project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Env{
		t:       t,
		home:    home,
		project: project,
		service: fakeplatform.New(t),
		client:  proxytest.Client(t),
		config:  map[string]string{},
	}

	userdirs.Isolate(t.Setenv, home)
	t.Setenv("ANTHROPIC_BASE_URL", "")
	// Claude Code's own configuration is read by the CLI too: its session
	// files root and the managed settings that decide whether hooks load.
	// Both point into this test's tree, never at the developer's.
	claudesettings.Isolate(t.Setenv, home)
	// The file token backend keeps tests away from the developer's OS
	// keyring. The CLI always talks to this test's own fake service: a
	// call a test did not stub fails loudly and is recorded, instead of
	// timing out against an unroutable address. The proxy address is one
	// nothing listens on, never the fixed production address, so a
	// developer's own running proxy is never taken for this test's.
	t.Setenv(tokenstore.BackendEnv, "file")
	e.proxyAddr = proxytest.IdleAddr(t)
	t.Setenv(cli.ProxyAddrEnv, e.proxyAddr)
	// Nothing this environment runs may start a process of its own. A
	// session hook starts the proxy, and a reader for the files it
	// registered, in processes that outlive the command, and a suite
	// that drives the CLI in its own process has no way to stop what it
	// left behind: the proxies accumulate until each one exits on idle.
	// Each start is recorded here instead, and Spawns reads back what
	// was recorded. The variable is passed on to every process a test
	// spawns, so a hook running in one of those leaves nothing behind
	// either.
	t.Setenv(cli.RecordProxyStartEnv, "1")
	e.SetPlatformURL(e.service.URL())
	return e
}

// ProxyAddr is where this environment's CLI looks for its proxy.
func (e *Env) ProxyAddr() string { return e.proxyAddr }

// Spawns is every process this environment's CLI reached a start for,
// in order, each named by what it was to run and by the address or
// directory it was given. None of them was started.
func (e *Env) Spawns() []proxylife.LoggedStart {
	e.t.Helper()
	starts, err := proxylife.LoggedStarts(e.Layout().ProxyLog())
	if err != nil {
		e.t.Fatal(err)
	}
	return starts
}

// ProxyStarts is the address of every proxy among them.
func (e *Env) ProxyStarts() []string {
	e.t.Helper()
	var addrs []string
	for _, s := range e.Spawns() {
		if s.Command == proxylife.Command {
			addrs = append(addrs, s.Target)
		}
	}
	return addrs
}

// SetPlatformURL points the CLI at a service endpoint by writing the
// user config file, the same source production reads. clitest never
// sets an environment variable for this: the CLI must not honor one.
func (e *Env) SetPlatformURL(url string) {
	e.t.Helper()
	e.setConfig("platform_url", url)
}

// SetReleasesURL points the CLI at a release source, through the same
// file and for the same reason: where a replacement binary comes from
// is the user's setting, not something a session's environment can
// choose.
func (e *Env) SetReleasesURL(url string) {
	e.t.Helper()
	e.setConfig("releases_url", url)
}

// setConfig rewrites the user config file with one key changed, leaving
// the keys an earlier call set in place.
func (e *Env) setConfig(key, value string) {
	e.t.Helper()
	e.config[key] = value
	data, err := json.Marshal(e.config)
	if err != nil {
		e.t.Fatal(err)
	}
	e.WriteConfig(string(data))
}

// WriteConfig replaces the user config file with content verbatim, so
// a test can plant what SetPlatformURL would never produce.
func (e *Env) WriteConfig(content string) {
	e.t.Helper()
	path := e.Layout().ConfigFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// Home is the temp directory standing in for the user's home.
func (e *Env) Home() string { return e.home }

// Project is the temp directory standing in for a working project.
func (e *Env) Project() string { return e.project }

// Service is the fake trajector service this environment's CLI talks
// to. Endpoints answer 590 until a test stubs them.
func (e *Env) Service() *fakeplatform.Server { return e.service }

// Paired stores a device token, as a completed login would.
func (e *Env) Paired() {
	e.t.Helper()
	if err := tokenstore.Files(e.Layout().SecretsDir()).SetDeviceToken("dev-tok-fake"); err != nil {
		e.t.Fatal(err)
	}
}

// At pins the CLI's clock to a fixed instant.
func (e *Env) At(at time.Time) {
	e.t.Setenv(cli.NowEnv, at.UTC().Format(time.RFC3339))
}

// ProjectRoot is the project directory in the form every derivation of
// a project identity uses.
func (e *Env) ProjectRoot() string {
	e.t.Helper()
	return proxytest.CanonicalRoot(e.t, e.project)
}

// ProjectHash is this project's identifier in stored records.
func (e *Env) ProjectHash() string {
	e.t.Helper()
	return proxytest.ProjectIDHash(e.ProjectRoot())
}

// Layout is where this environment keeps its trajector files, resolved
// exactly as the CLI will resolve them.
func (e *Env) Layout() userdirs.Layout {
	e.t.Helper()
	layout, err := userdirs.Resolve(userdirs.Host())
	if err != nil {
		e.t.Fatal(err)
	}
	return layout
}

// Sandbox reads and seeds the routing table and spool this environment's
// CLI shares with a proxy.
func (e *Env) Sandbox() *proxytest.Sandbox { return proxytest.Open(e.t, e.Layout()) }

// ProjectSettings is the settings file Claude Code reads in this
// environment's project, the one an enable writes into.
func (e *Env) ProjectSettings() *proxytest.Settings {
	e.t.Helper()
	return proxytest.ProjectSettings(e.t, e.ProjectRoot())
}

// Proxy is one in-process `trajector proxy serve` run, started through
// the CLI's own entry point so tests exercise the production assembly.
type Proxy struct {
	t       *testing.T
	addr    string
	layout  userdirs.Layout
	client  *http.Client
	stopped chan struct{}
}

// StartProxy serves the proxy on a free port, points the CLI at it, and
// returns once it answers healthz. It is stopped with the test if the
// test does not stop it first.
func (e *Env) StartProxy(extra ...string) *Proxy {
	e.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	e.proxyAddr = addr
	e.t.Setenv(cli.ProxyAddrEnv, addr)

	p := &Proxy{t: e.t, addr: addr, layout: e.Layout(), client: e.client, stopped: make(chan struct{})}
	args := append([]string{"proxy", "serve", "--addr", addr}, extra...)
	go func() {
		defer close(p.stopped)
		cli.Run(args, strings.NewReader(""), io.Discard, io.Discard)
	}()
	e.t.Cleanup(func() {
		select {
		case <-p.stopped:
		default:
			p.Stop()
		}
	})

	proxytest.WaitServing(e.t, e.client, addr, p.layout)
	return p
}

// Addr is the proxy's listen address.
func (p *Proxy) Addr() string { return p.addr }

// Stop drains the proxy and waits for it to exit.
func (p *Proxy) Stop() {
	p.t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+p.addr+apiproxy.DrainPath, nil)
	if err != nil {
		p.t.Fatal(err)
	}
	proxytest.Authorize(req, p.layout)
	resp, err := p.client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
	select {
	case <-p.stopped:
	case <-time.After(10 * time.Second):
		p.t.Error("proxy did not stop after drain")
	}
}

// Stopped reports the serve run ending on its own, for idle-exit tests.
func (p *Proxy) Stopped() <-chan struct{} { return p.stopped }

// Result captures one CLI invocation.
type Result struct {
	Exit   int
	Stdout string
	Stderr string
}

// Run executes the root command in-process with empty stdin.
func (e *Env) Run(args ...string) Result {
	return e.RunInput("", args...)
}

// RunInput executes the root command in-process, feeding input as
// stdin for interactive prompts.
func (e *Env) RunInput(input string, args ...string) Result {
	e.t.Helper()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(args, strings.NewReader(input), &stdout, &stderr)
	return Result{Exit: exit, Stdout: stdout.String(), Stderr: stderr.String()}
}

// RunToATerminal executes the root command with stdout on a character
// device — what the CLI reads as a terminal — and stderr in a buffer,
// so a test can observe what each stream was allowed to show. Nothing
// written to stdout is kept: the point of the run is what the other
// stream received.
func (e *Env) RunToATerminal(args ...string) Result {
	e.t.Helper()
	terminal, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	defer terminal.Close()
	if info, err := terminal.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		e.t.Skip("this platform's null device is not a character device")
	}
	e.t.Setenv("TERM", "xterm-256color")
	e.t.Setenv("LC_ALL", "en_US.UTF-8")
	var stderr bytes.Buffer
	exit := cli.Run(args, strings.NewReader(""), terminal, &stderr)
	return Result{Exit: exit, Stderr: stderr.String()}
}

// InProject runs the command with the working directory set to the
// project dir, matching how users invoke project-scoped commands.
func (e *Env) InProject(args ...string) Result {
	return e.InProjectInput("", args...)
}

// InProjectInput is InProject with stdin input.
func (e *Env) InProjectInput(input string, args ...string) Result {
	e.t.Helper()
	e.t.Chdir(e.project)
	return e.RunInput(input, args...)
}
