package lifecycle_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/platform"
)

func TestEnableWarnsWhenTheEndpointIsNotTheDefault(t *testing.T) {
	e := newEnv(t)
	e.startProxy()

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	out := e.stdout.String()
	if !strings.Contains(out, "not the default trajector service") {
		t.Errorf("stdout = %q, want the endpoint warning", out)
	}
	if !strings.Contains(out, e.service.URL()) {
		t.Errorf("stdout = %q, want the resolved endpoint named", out)
	}
}

func TestEnableStaysQuietOnTheDefaultEndpoint(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.deps.Platform = platform.New(platform.DefaultBaseURL, "testv")

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if out := e.stdout.String(); strings.Contains(out, "WARNING") {
		t.Errorf("stdout = %q, want no endpoint warning on the default", out)
	}
}

func TestServeProxyWarnsWhenTheEndpointIsNotTheDefault(t *testing.T) {
	e := newEnv(t)
	addr := freeAddr(t)
	e.deps.ProxyAddr = addr

	served := make(chan error, 1)
	go func() {
		served <- e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, e.stderr)
	}()
	waitHealthy(t, e, addr)
	drain := adminPost(t, e, "http://"+addr+apiproxy.DrainPath)
	drain.Body.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("ServeProxy = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("served proxy did not exit after a drain")
	}
	if !strings.Contains(e.stderr.String(), "not the default trajector service") {
		t.Errorf("stderr = %q, want the endpoint warning", e.stderr.String())
	}
}
