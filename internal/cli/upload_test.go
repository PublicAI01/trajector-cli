package cli_test

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// uploadEnv is a clitest environment with a fake service, a stored
// device token, and a helper to seed and reread the spool.
type uploadEnv struct {
	*clitest.Env
	service *fakeplatform.Server
}

func newUploadEnv(t *testing.T) *uploadEnv {
	t.Helper()
	e := newUploadEnvWithoutStubs(t)
	e.stubEchoAck(t)
	return e
}

func newUploadEnvWithoutStubs(t *testing.T) *uploadEnv {
	t.Helper()
	e := &uploadEnv{Env: clitest.New(t), service: fakeplatform.New(t)}
	t.Setenv("TRAJECTOR_PLATFORM_URL", e.service.URL())
	if err := tokenstore.Files(e.Layout().SecretsDir()).Save(tokenstore.DeviceTokenName, []byte("dev-tok-fake")); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *uploadEnv) stubEchoAck(t *testing.T) {
	t.Helper()
	e.service.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		parts, err := fakeplatform.Parts(r)
		if err != nil {
			return fakeplatform.JSON(590, map[string]any{"error": err.Error()})
		}
		var env struct {
			BatchID string `json:"batch_id"`
		}
		if err := json.Unmarshal(parts["batch"], &env); err != nil || env.BatchID == "" {
			return fakeplatform.JSON(590, map[string]any{"error": "no batch id in envelope"})
		}
		return fakeplatform.JSON(200, map[string]any{"batch_id": env.BatchID})
	})
}

func (e *uploadEnv) seedRawcall(t *testing.T, id string, at time.Time) {
	t.Helper()
	env, err := envelope.Record(envelope.Observation{
		Provider:          "anthropic",
		Endpoint:          "/v1/messages",
		HTTPStatus:        200,
		ClientVersion:     "test",
		ProjectIDHash:     e.ProjectHash(),
		At:                at,
		Upstream:          "https://api.anthropic.com",
		OfficialUpstream:  "https://api.anthropic.com",
		Request:           []byte(`{"model":"m","metadata":{"user_id":"sess"},"messages":[{"role":"user","content":"hello"}]}`),
		RequestComplete:   true,
		Response:          []byte(`{"id":"` + id + `","type":"message"}`),
		ResponseComplete:  true,
		ContentType:       "application/json",
		UpstreamRequestID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := spool.Create(e.Layout().SpoolDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.Write(env); err != nil {
		t.Fatal(err)
	}
}

func (e *uploadEnv) storedRawcalls(t *testing.T) int {
	t.Helper()
	sp, err := spool.Open(e.Layout().SpoolDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if err := sp.Each(func(spool.Rawcall) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	return n
}

// startProxy runs `trajector proxy serve` in-process on a free port —
// the production assembly path, uploader included — and returns once it
// answers healthz.
func (e *uploadEnv) startProxy(t *testing.T, extra ...string) (addr string, stopped chan struct{}) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = l.Addr().String()
	l.Close()
	t.Setenv("TRAJECTOR_PROXY_ADDR", addr)

	args := append([]string{"proxy", "serve", "--addr", addr}, extra...)
	stopped = make(chan struct{})
	go func() {
		defer close(stopped)
		cli.Run(args, strings.NewReader(""), &strings.Builder{}, &strings.Builder{})
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + apiproxy.HealthzPath)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return addr, stopped
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("proxy never became healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func stopProxy(t *testing.T, addr string, stopped chan struct{}) {
	t.Helper()
	resp, err := http.Post("http://"+addr+apiproxy.DrainPath, "", nil)
	if err == nil {
		resp.Body.Close()
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("proxy did not stop after drain")
	}
}

func TestUploadForceDrainsTheSpoolThroughTheProxy(t *testing.T) {
	e := newUploadEnv(t)
	e.seedRawcall(t, "req-1", time.Now().UTC())
	addr, stopped := e.startProxy(t)
	defer stopProxy(t, addr, stopped)

	got := e.Run("upload", "--force")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Uploaded 1 batch(es), 1 rawcall(s).") {
		t.Errorf("stdout = %q", got.Stdout)
	}
	if n := e.storedRawcalls(t); n != 0 {
		t.Errorf("spool holds %d rawcalls after an acknowledged upload", n)
	}
	uploads := 0
	for _, r := range e.service.Requests() {
		if strings.HasPrefix(r.URL, "/v1/batches") {
			uploads++
		}
	}
	if uploads != 1 {
		t.Errorf("service saw %d uploads, want 1", uploads)
	}
}

func TestUploadWithoutForceRespectsThresholds(t *testing.T) {
	e := newUploadEnv(t)
	e.seedRawcall(t, "req-1", time.Now().UTC())
	addr, stopped := e.startProxy(t)
	defer stopProxy(t, addr, stopped)

	got := e.Run("upload")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Below the upload thresholds") {
		t.Errorf("stdout = %q", got.Stdout)
	}
	if n := e.storedRawcalls(t); n != 1 {
		t.Errorf("spool holds %d rawcalls, want the record kept", n)
	}
}

func TestUploadWhileSignedOutPausesAndKeepsData(t *testing.T) {
	e := newUploadEnv(t)
	if err := tokenstore.Files(e.Layout().SecretsDir()).Delete(tokenstore.DeviceTokenName); err != nil {
		t.Fatal(err)
	}
	e.seedRawcall(t, "req-1", time.Now().UTC())
	addr, stopped := e.startProxy(t)
	defer stopProxy(t, addr, stopped)

	got := e.Run("upload", "--force")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "trajector login") {
		t.Errorf("stdout = %q", got.Stdout)
	}
	if n := e.storedRawcalls(t); n != 1 {
		t.Errorf("spool holds %d rawcalls, want the record kept", n)
	}
}

func TestIdleExitRunsAFinalFlush(t *testing.T) {
	e := newUploadEnv(t)
	e.seedRawcall(t, "req-1", time.Now().UTC().Add(-25*time.Hour))
	_, stopped := e.startProxy(t, "--idle-timeout", "150ms")

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("proxy did not idle out")
	}
	if n := e.storedRawcalls(t); n != 0 {
		t.Errorf("spool holds %d rawcalls after the final flush", n)
	}
	uploads := 0
	for _, r := range e.service.Requests() {
		if strings.HasPrefix(r.URL, "/v1/batches") {
			uploads++
		}
	}
	if uploads != 1 {
		t.Errorf("service saw %d uploads, want the final flush", uploads)
	}
}

func TestUploadSurfacesARejectedBatchLoudly(t *testing.T) {
	e := newUploadEnvWithoutStubs(t)
	e.service.Stub("POST", "/v1/batches", fakeplatform.JSON(400, map[string]any{"error": "bad multipart"}))
	e.seedRawcall(t, "req-1", time.Now().UTC())
	addr, stopped := e.startProxy(t)
	defer stopProxy(t, addr, stopped)

	got := e.Run("upload", "--force")
	if got.Exit != 1 {
		t.Fatalf("exit = %d (stdout: %q)", got.Exit, got.Stdout)
	}
	if !strings.Contains(got.Stderr, "rejected") || !strings.Contains(got.Stderr, "moved to") {
		t.Errorf("stderr = %q, want a loud rejection notice naming the rejected store", got.Stderr)
	}
	if n := e.storedRawcalls(t); n != 0 {
		t.Errorf("spool still holds %d rawcalls; the rejected batch must move aside", n)
	}
	if n, err := upload.RejectedCount(e.Layout().RejectedDir()); err != nil || n != 1 {
		t.Errorf("rejected store holds %d records (%v), want 1", n, err)
	}
}

func TestUploadUsage(t *testing.T) {
	e := clitest.New(t)
	if got := e.Run("upload", "--frobnicate"); got.Exit != 2 || !strings.Contains(got.Stderr, "usage: trajector upload") {
		t.Errorf("bad flag = %+v", got)
	}
}
