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
	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// Env is an isolated environment for one test.
type Env struct {
	t       *testing.T
	home    string
	project string
	service *fakeplatform.Server
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
	e := &Env{t: t, home: home, project: project, service: fakeplatform.New(t)}

	userdirs.Isolate(t.Setenv, home)
	t.Setenv("ANTHROPIC_BASE_URL", "")
	// The file token backend keeps tests away from the developer's OS
	// keyring. The CLI always talks to this test's own fake service: a
	// call a test did not stub fails loudly and is recorded, instead of
	// timing out against an unroutable address.
	t.Setenv(tokenstore.BackendEnv, "file")
	t.Setenv("TRAJECTOR_PROXY_ADDR", "")
	e.SetPlatformURL(e.service.URL())
	return e
}

// SetPlatformURL points the CLI at a service endpoint by writing the
// user config file, the same source production reads. clitest never
// sets an environment variable for this: the CLI must not honor one.
func (e *Env) SetPlatformURL(url string) {
	e.t.Helper()
	data, err := json.Marshal(map[string]string{"platform_url": url})
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

// ProjectHash is this project's identifier in stored records.
func (e *Env) ProjectHash() string {
	e.t.Helper()
	root, err := consent.CanonicalRoot(e.project)
	if err != nil {
		e.t.Fatal(err)
	}
	return consent.ProjectIDHash(root)
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

// Proxy is one in-process `trajector proxy serve` run, started through
// the CLI's own entry point so tests exercise the production assembly.
type Proxy struct {
	t       *testing.T
	addr    string
	layout  userdirs.Layout
	stopped chan struct{}
}

// adminRequest builds a request for a reserved proxy endpoint, carrying
// the admin token once the serving proxy has published it.
func adminRequest(t *testing.T, method, url string, layout userdirs.Layout) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxytest.Authorize(req, layout)
	return req
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
	e.t.Setenv(cli.ProxyAddrEnv, addr)

	p := &Proxy{t: e.t, addr: addr, layout: e.Layout(), stopped: make(chan struct{})}
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

	deadline := time.Now().Add(10 * time.Second)
	for {
		req := adminRequest(e.t, http.MethodGet, "http://"+addr+apiproxy.HealthzPath, p.layout)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatal("proxy never became healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Addr is the proxy's listen address.
func (p *Proxy) Addr() string { return p.addr }

// Stop drains the proxy and waits for it to exit.
func (p *Proxy) Stop() {
	p.t.Helper()
	req := adminRequest(p.t, http.MethodPost, "http://"+p.addr+apiproxy.DrainPath, p.layout)
	resp, err := http.DefaultClient.Do(req)
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
