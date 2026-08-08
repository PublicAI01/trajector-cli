package lifecycle_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

// servedProxy runs the machine's own proxy assembly for the test and
// drains it on cleanup.
func servedProxy(t *testing.T, e *env) {
	t.Helper()
	addr := freeAddr(t)
	e.deps.ProxyAddr = addr
	served := make(chan error, 1)
	go func() {
		served <- e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	}()
	waitHealthy(t, e, addr)
	t.Cleanup(func() {
		resp := adminPost(t, e, "http://"+addr+apiproxy.DrainPath)
		resp.Body.Close()
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Error("served proxy did not exit after a drain")
		}
	})
}

func TestUploadReportsEachOutcomeThroughTheResidentProxy(t *testing.T) {
	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", ackBatch(nil))
	servedProxy(t, e)
	m := e.machine()

	if err := m.Upload(true, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), "Nothing to upload.") {
		t.Errorf("stdout = %q", e.stdout)
	}

	e.stdout.Reset()
	e.sandbox.SeedRawcall("req-1", "hash-p1", time.Now().UTC())
	if err := m.Upload(false, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), "Below the upload thresholds") {
		t.Errorf("stdout = %q", e.stdout)
	}

	e.stdout.Reset()
	if err := m.Upload(true, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), "Uploaded 1 batch(es), 1 rawcall(s).") {
		t.Errorf("stdout = %q", e.stdout)
	}

	e.stdout.Reset()
	if err := e.deps.Tokens.ClearDeviceToken(); err != nil {
		t.Fatal(err)
	}
	e.sandbox.SeedRawcall("req-2", "hash-p1", time.Now().UTC())
	if err := m.Upload(true, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), "trajector login") {
		t.Errorf("stdout = %q", e.stdout)
	}
}

func TestUploadRefusesWhenThePortIsForeign(t *testing.T) {
	e := newEnv(t)
	e.occupyPort()

	if err := e.machine().Upload(true, e.io()); err == nil {
		t.Error("Upload against a foreign port holder = nil, want a loud failure")
	}
}

func TestUploadReportsAnUnverifiableProxyAsAuthentication(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.proxyEnv.AdminToken()
	proxytest.RemoveAdminTokens(t, e.layout(), e.proxyEnv.Addr())

	err := e.machine().Upload(true, e.io())
	if !errors.Is(err, lifecycle.ErrProxyUnverified) {
		t.Errorf("Upload = %v, want the authentication failure surfaced", err)
	}
}

func TestUploadNamesTheProxyOnAnUnreadableFlushReply(t *testing.T) {
	e := newEnv(t)
	e.startProxy(proxytest.WithInternal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not json")
	})))

	err := e.machine().Upload(true, e.io())
	if err == nil {
		t.Fatal("Upload = nil, want the unreadable flush reply reported")
	}
	if !strings.Contains(err.Error(), "proxy at "+e.proxyEnv.Addr()) {
		t.Errorf("Upload = %v, want the proxy named", err)
	}
}

func TestUploadExplainsAServiceDeferralFromTheFirstEncounter(t *testing.T) {
	e := newEnv(t)
	e.service.Stub("POST", "/v1/batches", fakeplatform.Response{
		Status: 429,
		Header: http.Header{"Retry-After": {"3600"}},
		Body:   []byte(`{"error":"slow down"}`),
	})
	servedProxy(t, e)
	m := e.machine()

	e.sandbox.SeedRawcall("req-1", "hash-p1", time.Now().UTC())
	if err := m.Upload(true, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), "asked to slow down") {
		t.Errorf("stdout = %q", e.stdout)
	}

	e.stdout.Reset()
	if err := m.Upload(false, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), "asked to slow down") {
		t.Errorf("stdout = %q", e.stdout)
	}
}

func TestUploadExplainsARequiredUpgradeFromTheFirstEncounter(t *testing.T) {
	e := newEnv(t)
	e.service.Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{
		"min_client_version": "9.9.9",
	}))
	servedProxy(t, e)
	m := e.machine()

	e.sandbox.SeedRawcall("req-1", "hash-p1", time.Now().UTC())
	if err := m.Upload(true, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), "requires trajector 9.9.9 or newer") {
		t.Errorf("stdout = %q", e.stdout)
	}
	if !strings.Contains(e.stdout.String(), "Captured data is kept.") {
		t.Errorf("stdout = %q", e.stdout)
	}

	// The required version reaches the user through the flush reply, not
	// by reading the proxy's handshake file across processes.
	if err := os.Remove(filepath.Join(e.layout().UploadDir(), "handshake.json")); err != nil {
		t.Fatal(err)
	}

	e.stdout.Reset()
	if err := m.Upload(false, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), "requires trajector 9.9.9 or newer") {
		t.Errorf("stdout = %q", e.stdout)
	}
	if !strings.Contains(e.stdout.String(), "Captured data is kept.") {
		t.Errorf("stdout = %q", e.stdout)
	}
}

func TestUploadReportsProgressBeforeAPauseStopsTheDrain(t *testing.T) {
	e := newEnv(t)
	e.service.StubFunc("POST", "/v1/batches", ackBatch(map[string]any{"flush_bytes": 1}))
	e.service.StubFunc("POST", "/v1/batches", ackBatch(nil))
	e.service.Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{
		"min_client_version": "9.9.9",
	}))
	servedProxy(t, e)
	m := e.machine()

	// The first upload's handshake caps batches at one record each, so
	// the next drain acknowledges one batch before the service pauses it.
	e.sandbox.SeedRawcall("req-1", "hash-p1", time.Now().UTC())
	if err := m.Upload(true, e.io()); err != nil {
		t.Fatal(err)
	}

	e.sandbox.SeedRawcall("req-2", "hash-p1", time.Now().UTC())
	e.sandbox.SeedRawcall("req-3", "hash-p1", time.Now().UTC())
	e.stdout.Reset()
	if err := m.Upload(true, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), "Uploaded 1 batch(es), 1 rawcall(s).") {
		t.Errorf("stdout = %q, want the acknowledged batch reported", e.stdout)
	}
	if !strings.Contains(e.stdout.String(), "Uploads are paused") {
		t.Errorf("stdout = %q, want the pause explained", e.stdout)
	}
	if n := len(e.sandbox.Rawcalls()); n != 1 {
		t.Errorf("spool holds %d rawcalls, want the unacknowledged record kept", n)
	}
}
