package lifecycle_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/report"
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
	tokens   *tokenstore.Store
	service  *fakeplatform.Server
	project  string
	stdin    string
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
	environ  map[string]string
	sandbox  *proxytest.Sandbox
	proxyEnv *proxytest.Env
	client   *http.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	layout := proxytest.SandboxLayout(t, t.TempDir())
	// The file token backend keeps these tests away from the developer's
	// OS keyring; the machine opens the store itself, so this is the one
	// place that can choose which backend it opens.
	t.Setenv(tokenstore.BackendEnv, "file")
	e := &env{
		t:       t,
		service: fakeplatform.New(t),
		project: t.TempDir(),
		stdin:   "yes\n",
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
		environ: map[string]string{},
		sandbox: proxytest.Open(t, layout),
		client:  proxytest.Client(t),
		tokens:  tokenstore.Files(layout.SecretsDir()),
	}
	e.deps = lifecycle.Deps{
		Layout:      layout,
		PlatformURL: e.service.URL(),
		Version:     "testv",
		ExecPath:    home + "/bin/trajector",
		Home:        home,
		Getenv:      func(key string) string { return e.environ[key] },
		Now:         func() time.Time { return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC) },
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
	if err := e.tokens.ClearDeviceToken(); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) machine() *lifecycle.Machine {
	e.t.Helper()
	return lifecycle.Open(e.deps)
}

func (e *env) io() lifecycle.IO {
	return lifecycle.IO{In: strings.NewReader(e.stdin), Out: e.stdout, Err: e.stderr}
}

// startProxy serves a real capture proxy against this device's routing
// table and spool, so a self-check runs against the genuine article.
// It announces this build's version unless an option says otherwise.
func (e *env) startProxy(opts ...proxytest.Option) {
	e.t.Helper()
	base := []proxytest.Option{proxytest.WithLayout(e.deps.Layout), proxytest.WithVersion(e.deps.Version)}
	e.proxyEnv = proxytest.New(e.t, append(base, opts...)...)
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

// occupyPortWithHealthzCopy binds the proxy address with a listener
// that answers exactly what this device's proxy would answer, while a
// published admin token is at stake on disk.
func (e *env) occupyPortWithHealthzCopy() *proxytest.Imposter {
	e.t.Helper()
	im := proxytest.StartImposter(e.t, proxytest.Health{Service: apiproxy.ServiceName, Version: e.deps.Version})
	proxytest.PublishAdminToken(e.t, e.deps.Layout, im.Addr(), "feedfacefeedfacefeedfacefeedface")
	e.deps.ProxyAddr = im.Addr()
	return im
}

// occupyPortStillPublishing binds the proxy address with a holder that
// leaves the first challenge unproven and proves itself from the next
// one on, as a sibling between winning its bind and publishing its
// admin token would.
func (e *env) occupyPortStillPublishing() *proxytest.Imposter {
	e.t.Helper()
	im := proxytest.StartImposter(e.t, proxytest.Health{Service: apiproxy.ServiceName, Version: e.deps.Version})
	const token = "feedfacefeedfacefeedfacefeedface"
	proxytest.PublishAdminToken(e.t, e.deps.Layout, im.Addr(), token)
	im.ProveAfter(1, token)
	e.deps.ProxyAddr = im.Addr()
	return im
}

// obstruct replaces a directory the machine expects with a plain file,
// so opening or listing it fails on every platform until the
// obstruction is removed.
func (e *env) obstruct(dir string) {
	e.t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		e.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		e.t.Fatal(err)
	}
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
func (e *env) status() report.ProjectStatus {
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

// gitRepo turns the project into a git repository, isolated from the
// developer's global git configuration so ignore coverage comes only
// from the repository itself. Skips the test when git is unavailable.
func (e *env) gitRepo() {
	e.t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		e.t.Skip("git not available")
	}
	e.t.Setenv("HOME", e.deps.Home)
	e.t.Setenv("XDG_CONFIG_HOME", filepath.Join(e.deps.Home, ".config"))
	e.t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(e.deps.Home, "gitconfig"))
	e.t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = e.project
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("git init: %v\n%s", err, out)
	}
}

// gitIgnored reports whether git ignores path inside the project.
func (e *env) gitIgnored(path string) bool {
	e.t.Helper()
	check := exec.Command("git", "check-ignore", "-q", "--", path)
	check.Dir = e.canonicalRoot()
	err := check.Run()
	if err == nil {
		return true
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false
	}
	e.t.Fatalf("git check-ignore: %v", err)
	return false
}

// pairable stubs a complete pairing flow on the fake service.
func (e *env) pairable() {
	e.t.Helper()
	e.service.PairableAs("pair-1", "dev-tok-fake")
}

// uploadedBatch is one recorded upload: which batch id carried which
// spool records.
type uploadedBatch struct {
	BatchID    string
	RequestIDs []string
}

// parseBatch reads the batch id and request ids one upload carried.
func parseBatch(r fakeplatform.Request) (uploadedBatch, error) {
	parts, err := fakeplatform.Parts(r)
	if err != nil {
		return uploadedBatch{}, err
	}
	var env struct {
		BatchID string `json:"batch_id"`
		Records []struct {
			RequestID string `json:"request_id"`
		} `json:"records"`
	}
	if err := json.Unmarshal(parts["batch"], &env); err != nil {
		return uploadedBatch{}, err
	}
	if env.BatchID == "" {
		return uploadedBatch{}, errors.New("no batch id in envelope")
	}
	b := uploadedBatch{BatchID: env.BatchID}
	for _, rec := range env.Records {
		b.RequestIDs = append(b.RequestIDs, rec.RequestID)
	}
	return b, nil
}

// ackBatch acknowledges an upload under the batch id it carried, the
// way the live service answers a well-formed batch; extra fields ride
// along in the acknowledgement body.
//
// TODO: converge the three sibling ack builders (echoAck in upload,
// stubEchoAck in cli, ackBatch here) into fakeplatform the next time
// ack semantics change.
func ackBatch(extra map[string]any) func(fakeplatform.Request) fakeplatform.Response {
	return func(r fakeplatform.Request) fakeplatform.Response {
		b, err := parseBatch(r)
		if err != nil {
			return fakeplatform.JSON(590, map[string]any{"error": err.Error()})
		}
		body := map[string]any{"batch_id": b.BatchID}
		for k, v := range extra {
			body[k] = v
		}
		return fakeplatform.JSON(200, body)
	}
}

func (e *env) seedDeviceToken() {
	e.t.Helper()
	if err := e.tokens.SetDeviceToken("dev-tok-fake"); err != nil {
		e.t.Fatal(err)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func waitHealthy(t *testing.T, e *env, addr string) {
	t.Helper()
	proxytest.WaitServing(t, e.client, addr, e.deps.Layout)
}

// adminPost posts to a served proxy's reserved endpoint with the admin
// token it published.
func adminPost(t *testing.T, e *env, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxytest.Authorize(req, e.deps.Layout)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
