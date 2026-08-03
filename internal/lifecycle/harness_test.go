package lifecycle_test

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// env is one isolated device: temp directories standing in for the
// user's machine, a fake service, and — once startProxy runs — a real
// capture proxy serving the same routing table and spool the machine
// writes.
type env struct {
	t        *testing.T
	deps     lifecycle.Deps
	service  *fakeplatform.Server
	project  string
	stdin    string
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
	environ  map[string]string
	sandbox  *proxytest.Sandbox
	proxyEnv *proxytest.Env
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	layout := proxytest.SandboxLayout(t, t.TempDir())
	e := &env{
		t:       t,
		service: fakeplatform.New(t),
		project: t.TempDir(),
		stdin:   "yes\n",
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
		environ: map[string]string{},
		sandbox: proxytest.Open(t, layout),
	}
	e.deps = lifecycle.Deps{
		Layout:   layout,
		Tokens:   tokenstore.Files(layout.SecretsDir()),
		Platform: platform.New(e.service.URL(), "testv"),
		Version:  "testv",
		ExecPath: home + "/bin/trajector",
		Home:     home,
		Getenv:   func(key string) string { return e.environ[key] },
		Now:      func() time.Time { return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC) },
	}
	// Most of what the machine does presumes a paired device; the tests
	// that care about pairing itself start from newUnpairedEnv.
	e.seedDeviceToken()
	return e
}

// newUnpairedEnv is newEnv without a stored device token.
func newUnpairedEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	if err := e.deps.Tokens.ClearDeviceToken(); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) machine() *lifecycle.Machine {
	e.t.Helper()
	m, err := lifecycle.Open(e.deps)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) io() lifecycle.IO {
	return lifecycle.IO{In: strings.NewReader(e.stdin), Out: e.stdout, Err: e.stderr}
}

// startProxy serves a real capture proxy against this device's routing
// table and spool, so a self-check runs against the genuine article.
func (e *env) startProxy() {
	e.t.Helper()
	e.proxyEnv = proxytest.New(e.t, proxytest.WithLayout(e.deps.Layout), proxytest.WithVersion("testv"))
	e.deps.ProxyAddr = e.proxyEnv.Addr()
}

// occupyPort binds the proxy address with a server that is not a
// trajector proxy.
func (e *env) occupyPort() {
	e.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { l.Close() })
	go http.Serve(l, http.NotFoundHandler())
	e.deps.ProxyAddr = l.Addr().String()
}

func (e *env) canonicalRoot() string {
	e.t.Helper()
	root, err := consent.CanonicalRoot(e.project)
	if err != nil {
		e.t.Fatal(err)
	}
	return root
}

func (e *env) settingsPath() string {
	return claudesettings.ProjectLocalPath(e.canonicalRoot())
}

// status is the machine's own answer about the project, the read half
// every assertion goes through instead of re-deriving state from the
// underlying stores.
func (e *env) status() lifecycle.ProjectStatus {
	e.t.Helper()
	st, err := e.machine().Project(e.project)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *env) consentStore() *consent.Store {
	return consent.Open(e.deps.Layout.ConsentFile())
}

func (e *env) layout() userdirs.Layout { return e.deps.Layout }

func (e *env) userSettingsContents() string {
	e.t.Helper()
	data, err := os.ReadFile(claudesettings.UserSettingsPath(e.deps.Home))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data)
}

func (e *env) consentFileContents() string {
	e.t.Helper()
	data, err := os.ReadFile(e.deps.Layout.ConsentFile())
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data)
}

// pairable stubs a complete pairing flow on the fake service.
func (e *env) pairable() {
	e.t.Helper()
	e.service.Stub("POST", "/v1/pairings", fakeplatform.JSON(200, map[string]any{
		"pairing_id":       "pair-1",
		"verification_url": "https://example.com/pair/pair-1",
		"user_code":        "ABCD-1234",
		"poll_interval_ms": 1,
	}))
	e.service.Stub("GET", "/v1/pairings/pair-1", fakeplatform.JSON(200, map[string]any{
		"status":       "paired",
		"device_token": "dev-tok-fake",
	}))
}

func (e *env) seedDeviceToken() {
	e.t.Helper()
	if err := e.deps.Tokens.SetDeviceToken("dev-tok-fake"); err != nil {
		e.t.Fatal(err)
	}
}
