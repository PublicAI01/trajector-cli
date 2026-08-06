package lifecycle_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
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
	waitHealthy(t, addr)
	t.Cleanup(func() {
		resp, err := http.Post("http://"+addr+apiproxy.DrainPath, "", nil)
		if err == nil {
			resp.Body.Close()
		}
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Error("served proxy did not exit after a drain")
		}
	})
}

func TestUploadReportsEachOutcomeThroughTheResidentProxy(t *testing.T) {
	e := newEnv(t)
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

func TestUploadExplainsAServiceDeferral(t *testing.T) {
	e := newEnv(t)
	e.service.Stub("POST", "/v1/batches", fakeplatform.Response{
		Status: 429,
		Header: http.Header{"Retry-After": {"3600"}},
		Body:   []byte(`{"error":"slow down"}`),
	})
	servedProxy(t, e)
	m := e.machine()

	e.sandbox.SeedRawcall("req-1", "hash-p1", time.Now().UTC())
	if err := m.Upload(true, e.io()); err == nil {
		t.Fatal("a rate-limited batch upload reported success")
	}

	e.stdout.Reset()
	if err := m.Upload(false, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stdout.String(), "asked to slow down") {
		t.Errorf("stdout = %q", e.stdout)
	}
}

func TestUploadExplainsARequiredUpgrade(t *testing.T) {
	e := newEnv(t)
	e.service.Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{
		"min_client_version": "9.9.9",
	}))
	servedProxy(t, e)
	m := e.machine()

	e.sandbox.SeedRawcall("req-1", "hash-p1", time.Now().UTC())
	if err := m.Upload(true, e.io()); err == nil {
		t.Fatal("a refused batch upload reported success")
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
