package proxylife_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// serveProxy is the only behavior the spawned process tree needs: parse
// the argv proxylife itself constructs, and be the proxy through the
// composition root, the same assembly the CLI would run. The service
// URL is unroutable, so the resident uploader never reaches a network.
func serveProxy(args []string) int {
	if len(args) < 4 || args[0] != proxylife.Command {
		return 96
	}
	addr := args[3]
	layout, err := userdirs.Resolve(userdirs.Host())
	if err != nil {
		return 95
	}
	exe, err := os.Executable()
	if err != nil {
		return 95
	}
	m, err := lifecycle.Open(lifecycle.Deps{
		Layout:    layout,
		Tokens:    tokenstore.Files(layout.SecretsDir()),
		Platform:  platform.New("http://127.0.0.1:1", "dev"),
		Version:   "dev",
		ExecPath:  exe,
		ProxyAddr: addr,
	})
	if err != nil {
		return 94
	}
	ctx := context.Background()
	if args[1] == proxylife.Supervise {
		err = m.SuperviseProxy(ctx, 30*time.Second, os.Stdout, os.Stderr)
	} else {
		err = m.ServeProxy(ctx, 30*time.Second, os.Stdout, os.Stderr)
	}
	if err != nil {
		return 1
	}
	return 0
}

// markerEnv names the file a crash-once child touches, since the
// watchdog owns the child's argv.
const markerEnv = "TRAJECTOR_TEST_CRASH_MARKER"

func TestMain(m *testing.M) {
	procbin.Main(m, map[string]func(args []string) int{
		"proxy":        serveProxy,
		"exit-clean":   func([]string) int { return 0 },
		"always-crash": func([]string) int { return 1 },
		"sleep": func([]string) int {
			time.Sleep(time.Minute)
			return 0
		},
		"crash-until-marker": func([]string) int {
			marker := os.Getenv(markerEnv)
			if _, err := os.Stat(marker); err == nil {
				return 0
			}
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				return 3
			}
			return 1
		},
	})
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

// supervised describes a proxy this test may actually spawn, in an
// isolated sandbox, and stops whatever it started.
func supervised(t *testing.T, addr string) *proxylife.Proxy {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	layout := proxytest.SandboxLayout(t, dir)
	p := proxylife.For(layout, "dev", procbin.Self(t, "proxy"), addr)
	t.Cleanup(func() {
		p.Stop()
		deadline := time.Now().Add(10 * time.Second)
		for {
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err != nil {
				return
			}
			conn.Close()
			if time.Now().After(deadline) {
				t.Error("spawned proxy did not release its port")
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return p
}

func TestEnsureStartsSupervisedProxyAndIsIdempotent(t *testing.T) {
	p := supervised(t, freeAddr(t))

	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	h, running := p.Health()
	if !running || h.Service != apiproxy.ServiceName || h.Version != "dev" {
		t.Fatalf("after Ensure: running=%v health=%+v", running, h)
	}

	if err := p.Ensure(); err != nil {
		t.Errorf("second Ensure: %v, want nil against the healthy instance", err)
	}
}

func TestEnsureReplacesStaleVersionViaDrain(t *testing.T) {
	addr := freeAddr(t)
	stale := proxytest.New(t, proxytest.WithAddr(addr), proxytest.WithVersion("0.0.9"))
	p := supervised(t, addr)

	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := stale.WaitStopped(5 * time.Second); err != nil {
		t.Errorf("stale proxy Serve returned %v on takeover, want nil", err)
	}
	h, running := p.Health()
	if !running || h.Version != "dev" {
		t.Errorf("after takeover: running=%v health=%+v, want version dev", running, h)
	}
}

func TestEnsureRefusesForeignPortHolder(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go http.Serve(l, http.NotFoundHandler())

	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", l.Addr().String())
	if err := p.Ensure(); !errors.Is(err, proxylife.ErrPortOccupied) {
		t.Errorf("Ensure = %v, want ErrPortOccupied", err)
	}
}

func TestHealthReportsNothingRunning(t *testing.T) {
	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", freeAddr(t))
	if _, running := p.Health(); running {
		t.Error("Health reports a listener on a closed port")
	}
}

func TestStopOnNothingListeningIsANoop(t *testing.T) {
	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", freeAddr(t))
	p.Stop()
	p.Stop()
}

// The supervisor is exercised through Supervise, with the spawned child
// standing in for the proxy: the watchdog cannot tell them apart.
func superviseWith(t *testing.T, behavior string) *proxylife.Proxy {
	t.Helper()
	return proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", procbin.Self(t, behavior), freeAddr(t))
}

func TestSuperviseEndsWithCleanChildExit(t *testing.T) {
	err := superviseWith(t, "exit-clean").Supervise(context.Background(), 0, os.Stdout, os.Stderr)
	if err != nil {
		t.Errorf("Supervise = %v, want nil for a clean child exit", err)
	}
}

func TestSuperviseRestartsCrashedChildUntilCleanExit(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "crashed-once")
	t.Setenv(markerEnv, marker)
	err := superviseWith(t, "crash-until-marker").Supervise(context.Background(), 0, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatalf("Supervise = %v, want nil after a restart recovers", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("child never crashed; the restart path went untested")
	}
}

func TestSuperviseGivesUpOnCrashLoop(t *testing.T) {
	err := superviseWith(t, "always-crash").Supervise(context.Background(), 0, os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "giving up") {
		t.Errorf("Supervise = %v, want a crash-loop error", err)
	}
}

func TestSuperviseStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := superviseWith(t, "sleep").Supervise(ctx, 0, os.Stdout, os.Stderr)
	if err == nil {
		t.Error("Supervise = nil, want the context error")
	}
	if time.Since(start) > 10*time.Second {
		t.Error("the watchdog kept the child long after cancellation")
	}
}

func TestSelfcheckErrorNeverCarriesTheToken(t *testing.T) {
	// A failed selfcheck must not leak the project token: the token rides
	// in the request URL, and a transport error's default message embeds
	// that URL. Callers print this error to stderr.
	layout, err := userdirs.Resolve(userdirs.Host())
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens now: the selfcheck GET is refused

	const token = "deadbeefdeadbeefdeadbeefdeadbeef"
	p := proxylife.For(layout, "test", "/nonexistent/trajector", addr)
	_, err = p.Selfcheck(token)
	if err == nil {
		t.Fatal("selfcheck against a dead address unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("selfcheck error leaked the token: %v", err)
	}
}
