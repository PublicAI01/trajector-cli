package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

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

func waitHealthy(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + apiproxy.HealthzPath)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("served proxy never became healthy")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServeProxyHostsCaptureAndTheFlushEndpoint(t *testing.T) {
	e := newEnv(t)
	addr := freeAddr(t)
	e.deps.ProxyAddr = addr
	e.sandbox.SeedRawcall("req-1", "hash-p1", time.Now().UTC())
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

	served := make(chan error, 1)
	go func() {
		served <- e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	}()
	waitHealthy(t, addr)

	resp, err := http.Post("http://"+addr+upload.FlushPath+"?force=1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reply upload.FlushReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Service != apiproxy.ServiceName || reply.Outcome != upload.Uploaded || reply.Records != 1 {
		t.Errorf("flush reply = %+v, want the seeded rawcall uploaded", reply)
	}

	if err := e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard); err != nil {
		t.Errorf("second ServeProxy = %v, want quiet deferral to the healthy proxy", err)
	}

	drain, err := http.Post("http://"+addr+apiproxy.DrainPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	drain.Body.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("ServeProxy = %v after a drain, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("served proxy did not exit after a drain")
	}
}

func TestProjectSurfacesAnUnreadableTable(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(filepath.Dir(e.deps.Layout.RoutingTable()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.deps.Layout.RoutingTable(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := e.machine().Project(e.project); err == nil {
		t.Error("Project with an unreadable table = nil, want the error surfaced")
	}
}

func TestAnUnreadableTokenStoreReadsAsSignedOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not block reads on windows")
	}
	e := newEnv(t)
	if err := os.Chmod(e.deps.Layout.SecretsDir(), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(e.deps.Layout.SecretsDir(), 0o700) })

	if e.machine().Paired() {
		t.Error("an unreadable token store reported the device as paired")
	}
}

func TestServeProxyRefusesAForeignPortHolder(t *testing.T) {
	e := newEnv(t)
	e.occupyPort()

	err := e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	if !errors.Is(err, lifecycle.ErrPortOccupied) {
		t.Errorf("ServeProxy = %v, want ErrPortOccupied", err)
	}
}

func TestServeProxySurfacesASpoolFailure(t *testing.T) {
	e := newEnv(t)
	e.deps.ProxyAddr = freeAddr(t)
	if err := os.MkdirAll(filepath.Dir(e.deps.Layout.SpoolDir()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.deps.Layout.SpoolDir(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "spool") {
		t.Errorf("ServeProxy with a blocked spool = %v, want a spool error", err)
	}
}

func TestSuperviseProxyReturnsWhenTheContextIsAlreadyDone(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.machine().SuperviseProxy(ctx, 0, io.Discard, io.Discard)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("SuperviseProxy did not return on a dead context")
	}
}
