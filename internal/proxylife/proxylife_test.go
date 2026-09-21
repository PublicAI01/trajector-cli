package proxylife_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/proxyserve"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/upload"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// serveProxy is the only behavior the spawned process tree needs: parse
// the argv proxylife itself constructs, and be the proxy through the
// same assembly a serving process runs. The service URL is unroutable,
// so the resident uploader never reaches a network.
func serveProxy(args []string) int {
	if len(args) < 4 || args[0] != proxylife.Command {
		return 96
	}
	layout, err := userdirs.Resolve(userdirs.Host())
	if err != nil {
		return 95
	}
	exe, err := os.Executable()
	if err != nil {
		return 95
	}
	version := os.Getenv(versionEnv)
	if version == "" {
		version = "dev"
	}
	assembly := proxyserve.Assembly{
		Layout:   layout,
		Tokens:   tokenstore.Files(layout.SecretsDir()),
		Service:  platform.New("http://127.0.0.1:1", version),
		Consent:  consent.Open(layout.ConsentFile()),
		Version:  version,
		ExecPath: exe,
		Addr:     args[3],
	}
	ctx := context.Background()
	if args[1] == proxylife.Supervise {
		err = proxyserve.Supervise(ctx, assembly, 30*time.Second, os.Stdout, os.Stderr)
	} else {
		err = proxyserve.Serve(ctx, assembly, 30*time.Second, os.Stdout, os.Stderr)
	}
	if err != nil {
		return 1
	}
	return 0
}

// markerEnv names the file a crash-once child touches, since the
// watchdog owns the child's argv.
const markerEnv = "TRAJECTOR_TEST_CRASH_MARKER"

// silentEnv names the file whose presence keeps a spawned proxy from
// answering, since the watchdog owns the child's argv.
const silentEnv = "TRAJECTOR_TEST_SILENT_CHILD"

// holdSilently binds the address in the proxy's own argv and reads
// nothing from it. It stops on the signal a drain would otherwise ask
// for, and exits the way a child that has drained does.
func holdSilently(args []string) int {
	if len(args) < 4 {
		return 96
	}
	l, err := net.Listen("tcp", args[3])
	if err != nil {
		return 3
	}
	defer l.Close()
	go func() {
		var held []net.Conn
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			held = append(held, conn)
		}
	}()
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stopping:
		return 0
	case <-time.After(time.Minute):
		return 4
	}
}

// versionEnv sets the version the spawned proxy announces, since the
// watchdog owns the child's argv.
const versionEnv = "TRAJECTOR_TEST_PROXY_VERSION"

func TestMain(m *testing.M) {
	procbin.Main(m, map[string]func(args []string) int{
		"proxy":        serveProxy,
		"exit-clean":   func([]string) int { return 0 },
		"always-crash": func([]string) int { return 1 },
		"sleep": func([]string) int {
			time.Sleep(time.Minute)
			return 0
		},
		// Stands in for the serving proxy, whose whole shutdown path —
		// final upload flush, request drain, record-queue drain — hangs
		// off a catchable signal. It records that it was asked to stop
		// rather than killed, then exits cleanly the way the real child
		// does once it has drained.
		"graceful-stop": func([]string) int {
			stopping := make(chan os.Signal, 1)
			signal.Notify(stopping, os.Interrupt, syscall.SIGTERM)
			select {
			case <-stopping:
				if err := os.WriteFile(os.Getenv(markerEnv), nil, 0o600); err != nil {
					return 3
				}
				return 0
			case <-time.After(time.Minute):
				return 4
			}
		},
		// Stands in for a proxy of ours that stopped answering: it
		// holds the port and reads nothing from it, which is what a
		// hung process and a forwarded port look like from outside.
		// It stops on the signal a drain would otherwise ask for.
		"silent-hold": holdSilently,
		// Stands in for the same holder under the watchdog production
		// runs: the watchdog is the real one, and the child it starts
		// holds the port and reads nothing from it while the file named
		// by silentEnv is there.
		"supervised-silent-hold": func(args []string) int {
			if len(args) > 1 && args[1] == proxylife.Supervise {
				return serveProxy(args)
			}
			if _, err := os.Stat(os.Getenv(silentEnv)); err == nil {
				return holdSilently(args)
			}
			return serveProxy(args)
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
// isolated sandbox, and stops whatever it started. The layout is
// returned so a test can put another proxy on the same one — one user,
// one set of trajector files.
func supervised(t *testing.T, addr, version string) (*proxylife.Proxy, userdirs.Layout) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv(versionEnv, version)
	layout := proxytest.SandboxLayout(t, dir)
	p := proxylife.For(layout, version, procbin.Self(t, "proxy"), addr, nil)
	t.Cleanup(func() {
		p.Stop()
		waitReleased(t, addr)
		waitLogReleased(t, layout)
	})
	return p, layout
}

// waitLogReleased waits until the spawned process tree has let go of
// the proxy log. The port comes free when the serving child exits, but
// the supervisor holding the log's append handle exits a beat later —
// and Windows refuses the temp-dir cleanup's delete while any handle
// is open.
func waitLogReleased(t *testing.T, layout userdirs.Layout) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if logRemoved(layout) {
			return
		}
		if time.Now().After(deadline) {
			t.Log("the proxy log is still held at cleanup")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// logRemoved removes the proxy log, reporting whether it is gone — on
// Windows the remove is refused while any process holds the file open.
func logRemoved(layout userdirs.Layout) bool {
	err := os.Remove(layout.ProxyLog())
	return err == nil || errors.Is(err, fs.ErrNotExist)
}

// waitReleased waits for whatever holds addr to release it.
func waitReleased(t *testing.T, addr string) {
	t.Helper()
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
}

func TestEnsureStartsSupervisedProxyAndIsIdempotent(t *testing.T) {
	p, _ := supervised(t, freeAddr(t), "dev")

	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	v := p.Observe()
	if v.Holder != proxylife.HolderOurs || v.Health.Version != "dev" {
		t.Fatalf("after Ensure: holder=%v health=%+v", v.Holder, v.Health)
	}

	if err := p.Ensure(); err != nil {
		t.Errorf("second Ensure: %v, want nil against the healthy instance", err)
	}
}

func TestEnsureReplacesAStrictlyOlderReleaseViaDrain(t *testing.T) {
	addr := freeAddr(t)
	p, layout := supervised(t, addr, "1.0.0")
	older := proxytest.New(t, proxytest.WithAddr(addr), proxytest.WithVersion("0.0.9"), proxytest.WithLayout(layout))

	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := older.WaitStopped(5 * time.Second); err != nil {
		t.Errorf("older proxy Serve returned %v on takeover, want nil", err)
	}
	v := p.Observe()
	if v.Holder != proxylife.HolderOurs || v.Health.Version != "1.0.0" {
		t.Errorf("after takeover: holder=%v health=%+v, want version 1.0.0", v.Holder, v.Health)
	}
}

// servingHolder runs a real proxy announcing version on a fresh
// sandbox. Callers probe it with an unspawnable exec path, so Ensure
// returning nil proves it neither drained the holder nor tried to
// start a sibling.
func servingHolder(t *testing.T, version string) (userdirs.Layout, string) {
	t.Helper()
	addr := freeAddr(t)
	layout := proxytest.SandboxLayout(t, t.TempDir())
	proxytest.New(t, proxytest.WithAddr(addr), proxytest.WithVersion(version), proxytest.WithLayout(layout))
	return layout, addr
}

func proxyLogContents(t *testing.T, layout userdirs.Layout) string {
	t.Helper()
	data, err := os.ReadFile(layout.ProxyLog())
	if err != nil {
		t.Fatalf("reading proxy log: %v", err)
	}
	return string(data)
}

func TestEnsureReusesANewerProxyInsteadOfDrainingIt(t *testing.T) {
	layout, addr := servingHolder(t, "2.0.0")

	p := proxylife.For(layout, "1.0.0", "unspawnable", addr, nil)
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure = %v, want the newer proxy reused", err)
	}
	if v := p.Observe(); v.Holder != proxylife.HolderOurs || v.Health.Version != "2.0.0" {
		t.Errorf("after Ensure: holder=%v version=%q, want the newer proxy left serving", v.Holder, v.Health.Version)
	}
	if log := proxyLogContents(t, layout); !strings.Contains(log, proxylife.ReuseReason) {
		t.Errorf("proxy log = %q, want the reuse decision on record", log)
	}
}

func TestEnsureFromADevBuildReusesAReleaseProxy(t *testing.T) {
	layout, addr := servingHolder(t, "1.2.3")

	p := proxylife.For(layout, "dev", "unspawnable", addr, nil)
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure = %v, want the release proxy reused", err)
	}
	if v := p.Observe(); v.Holder != proxylife.HolderOurs || v.Health.Version != "1.2.3" {
		t.Errorf("after Ensure: holder=%v version=%q, want the release proxy left serving", v.Holder, v.Health.Version)
	}
	log := proxyLogContents(t, layout)
	if !strings.Contains(log, "reuses the version 1.2.3 proxy") || !strings.Contains(log, proxylife.ReuseReason) {
		t.Errorf("proxy log = %q, want the reuse decision on record with its reason", log)
	}
}

func TestEnsureFromAReleaseBuildReusesADevProxy(t *testing.T) {
	layout, addr := servingHolder(t, "dev")

	p := proxylife.For(layout, "9.9.9", "unspawnable", addr, nil)
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure = %v, want the dev proxy reused", err)
	}
	if v := p.Observe(); v.Holder != proxylife.HolderOurs || v.Health.Version != "dev" {
		t.Errorf("after Ensure: holder=%v version=%q, want the dev proxy left serving", v.Holder, v.Health.Version)
	}
	if log := proxyLogContents(t, layout); !strings.Contains(log, proxylife.ReuseReason) {
		t.Errorf("proxy log = %q, want the reuse decision on record", log)
	}
}

func TestOnlyAStrictlyOlderSemanticVersionIsReplaceable(t *testing.T) {
	cases := []struct {
		name         string
		ours, holder string
		want         bool
	}{
		{"older patch is replaced", "1.2.3", "1.2.2", true},
		{"older minor is replaced", "1.3.0", "1.2.9", true},
		{"older major is replaced", "2.0.0", "1.9.9", true},
		{"equal version is reused", "1.2.3", "1.2.3", false},
		{"newer patch is reused", "1.2.3", "1.2.4", false},
		{"newer major is reused", "1.2.3", "2.0.0", false},
		{"components order numerically not textually", "1.10.0", "1.9.0", true},
		{"leading v spells the same version", "v1.2.3", "1.2.2", true},
		{"leading v on the holder spells the same version", "1.2.3", "v1.2.4", false},
		{"build metadata never orders", "1.2.3+2", "1.2.3+1", false},
		{"a prerelease is older than its release", "1.2.3", "1.2.3-rc.1", true},
		{"a release is never replaced by its own prerelease", "1.2.3-rc.1", "1.2.3", false},
		{"a later prerelease replaces an earlier one", "1.2.3-rc.2", "1.2.3-rc.1", true},
		{"numeric prerelease identifiers order numerically", "1.2.3-rc.10", "1.2.3-rc.9", true},
		{"numeric identifiers are older than alphanumeric ones", "1.2.3-alpha", "1.2.3-99", true},
		{"a longer prerelease outranks its prefix", "1.2.3-rc.1.1", "1.2.3-rc.1", true},
		{"dev replaces no release", "dev", "0.0.1", false},
		{"dev is not replaced by any release", "99.0.0", "dev", false},
		{"dev does not replace dev", "dev", "dev", false},
		{"an empty holder version is not replaced", "1.2.3", "", false},
		{"an empty own version replaces nothing", "", "1.2.3", false},
		{"two-part versions have no order", "1.2", "1.1.9", false},
		{"four-part versions have no order", "1.2.3.4", "1.2.3", false},
		{"a commit hash is not replaced", "1.2.3", "4f9c2ab", false},
		{"a malformed prerelease has no order", "1.2.4", "1.2.3-rc_1", false},
		{"an empty prerelease has no order", "1.2.4", "1.2.3-", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := proxylife.Verdict{Holder: proxylife.HolderOurs, Health: proxylife.Health{Version: tc.holder}}
			if got := v.Replaceable(tc.ours); got != tc.want {
				t.Errorf("version %q held by version %q: Replaceable = %v, want %v", tc.ours, tc.holder, got, tc.want)
			}
			if got := v.Serving(tc.ours); got == tc.want {
				t.Errorf("version %q held by version %q: Serving = %v, want the holder either replaced or left serving, never both or neither", tc.ours, tc.holder, got)
			}
		})
	}
}

func TestAPortHeldByNobodyIsNeitherServingNorReplaceable(t *testing.T) {
	for _, holder := range []proxylife.Holder{proxylife.HolderNone, proxylife.HolderForeign} {
		t.Run(holder.String(), func(t *testing.T) {
			v := proxylife.Verdict{Holder: holder, Health: proxylife.Health{Version: "0.0.1"}}
			if v.Serving("1.2.3") || v.Replaceable("1.2.3") {
				t.Errorf("holder %v: Serving=%v Replaceable=%v, want no claim on a port no proxy of ours proved it holds", holder, v.Serving("1.2.3"), v.Replaceable("1.2.3"))
			}
		})
	}
}

func TestEnsureRefusesForeignPortHolder(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go http.Serve(l, http.NotFoundHandler())

	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", l.Addr().String(), nil)
	if err := p.Ensure(); !errors.Is(err, proxylife.ErrPortOccupied) {
		t.Errorf("Ensure = %v, want ErrPortOccupied", err)
	}
}

// healthzCopyHolder is a listener squatting the proxy address and
// answering exactly what a live proxy's healthz answers, probed on a
// layout where a published admin token is genuinely at stake.
func healthzCopyHolder(t *testing.T) (*proxylife.Proxy, *proxytest.Imposter) {
	t.Helper()
	layout := proxytest.SandboxLayout(t, t.TempDir())
	im := proxytest.StartImposter(t, proxytest.Health{Service: apiproxy.ServiceName, Version: "dev"})
	proxytest.PublishAdminToken(t, layout, im.Addr(), "feedfacefeedfacefeedfacefeedface")
	return proxylife.For(layout, "dev", "unused", im.Addr(), nil), im
}

func TestObserveTreatsAHealthzCopyAsForeign(t *testing.T) {
	p, im := healthzCopyHolder(t)
	v := p.Observe()
	if v.Holder != proxylife.HolderForeign {
		t.Errorf("holder = %v for a listener copying the health payload, want foreign", v.Holder)
	}
	if !errors.Is(v.Reason, proxylife.ErrPortOccupied) {
		t.Errorf("reason = %v, want the stranger verdict for a holder that offers no proof", v.Reason)
	}
	if im.SawHeader(apiproxy.AdminHeader) {
		t.Error("the admin token was sent to a holder that never proved it knows it")
	}
}

func TestEnsureRefusesAHealthzCopyingPortHolder(t *testing.T) {
	p, im := healthzCopyHolder(t)
	if err := p.Ensure(); !errors.Is(err, proxylife.ErrPortOccupied) {
		t.Errorf("Ensure = %v, want ErrPortOccupied", err)
	}
	if im.SawHeader(apiproxy.AdminHeader) {
		t.Error("the admin token was sent to a holder that never proved it knows it")
	}
}

// silentHolder squats an address and answers nothing: it accepts the
// connection and leaves it open, which is what a port forwarded
// elsewhere and a process that stopped reading both do.
func silentHolder(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			conn.Close()
		}
	})
	return l.Addr().String()
}

// wrongProofHolder answers every challenge with a proof of a token
// this device never published.
func wrongProofHolder(t *testing.T) *proxylife.Proxy {
	t.Helper()
	layout := proxytest.SandboxLayout(t, t.TempDir())
	im := proxytest.StartImposter(t, proxytest.Health{Service: apiproxy.ServiceName, Version: "dev"})
	proxytest.PublishAdminToken(t, layout, im.Addr(), "feedfacefeedfacefeedfacefeedface")
	im.ProveAfter(0, "0123456789abcdef0123456789abcdef")
	return proxylife.For(layout, "dev", "unused", im.Addr(), nil)
}

func TestObserveTellsThePortHolderShapesApart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy func(*testing.T) *proxylife.Proxy
		want  error
	}{
		{
			name: "the holder answers nothing",
			proxy: func(t *testing.T) *proxylife.Proxy {
				return proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", silentHolder(t), nil)
			},
			want: proxylife.ErrPortSilent,
		},
		{
			name: "the holder answers without proof",
			proxy: func(t *testing.T) *proxylife.Proxy {
				p, _ := healthzCopyHolder(t)
				return p
			},
			want: proxylife.ErrPortOccupied,
		},
		{
			name:  "the holder answers with a proof of another token",
			proxy: wrongProofHolder,
			want:  proxylife.ErrProxyUnverified,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.proxy(t).Observe()
			if v.Holder != proxylife.HolderForeign {
				t.Errorf("holder = %v, want foreign", v.Holder)
			}
			if !errors.Is(v.Reason, tc.want) {
				t.Errorf("reason = %v, want %v", v.Reason, tc.want)
			}
		})
	}
}

func TestASilentHolderNamesTheAddressItHolds(t *testing.T) {
	addr := silentHolder(t)
	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", addr, nil)

	got, held := proxylife.HeldPort(p.Observe().Reason)
	if !held || got != addr {
		t.Errorf("held port = %q, %v, want %q", got, held, addr)
	}
}

func TestNoHolderIsProvenByANameThatIsNoProgramFile(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "trajector")
	if err := os.WriteFile(exe, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	for _, tc := range []struct {
		name string
		exe  string
		want bool
	}{
		{name: "the program file the operating system named", exe: exe, want: true},
		{name: "a bare name the working directory resolves", exe: "trajector", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			holder := proxylife.Process{
				PID:  1,
				Name: "trajector",
				Exe:  tc.exe,
				Argv: []string{exe, proxylife.Command, proxylife.Serve, "--addr", "127.0.0.1:41100"},
			}
			if got := holder.IsProxyOf(exe); got != tc.want {
				t.Errorf("IsProxyOf = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHolderCommandAsksTheToolThisPlatformHas(t *testing.T) {
	want := map[string]string{
		"linux":   "ss -ltnp 'sport = :41100'",
		"windows": "netstat -ano | findstr 41100",
	}[runtime.GOOS]
	if want == "" {
		want = "lsof -nP -iTCP:41100 -sTCP:LISTEN"
	}
	for _, tc := range []struct {
		name string
		addr string
	}{
		{name: "an address with a host", addr: "127.0.0.1:41100"},
		{name: "an address that is only a port", addr: "41100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := proxylife.HolderCommand(tc.addr); got != want {
				t.Errorf("HolderCommand(%q) = %q, want %q", tc.addr, got, want)
			}
		})
	}
}

func TestEnsureLeavesASilentHolderItCannotProveIsOursAlone(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	addr := silentHolder(t)
	p := proxylife.For(layout, "dev", "/nonexistent/trajector", addr, proxylife.RecordStartsIn(layout.ProxyLog()))

	if err := p.Ensure(); !errors.Is(err, proxylife.ErrPortSilent) {
		t.Errorf("Ensure = %v, want the silent holder reported and left alone", err)
	}
	starts, err := proxylife.LoggedStarts(layout.ProxyLog())
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) != 0 {
		t.Errorf("starts = %v, want nothing started against a port this build may not take", starts)
	}
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err != nil {
		t.Errorf("the holder was stopped: %v", err)
	} else {
		conn.Close()
	}
}

func TestEnsureReplacesAProxyOfOursThatStoppedAnswering(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this device reads no holder on Windows, so no holder is ever proven ours")
	}
	layout := proxytest.SandboxLayout(t, t.TempDir())
	addr := freeAddr(t)
	exe := procbin.Self(t, "silent-hold")
	holder := exec.Command(exe, proxylife.Command, proxylife.Serve, "--addr", addr)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		holder.Process.Kill()
		holder.Wait()
	})
	waitListening(t, addr)
	p := proxylife.For(layout, "dev", exe, addr, proxylife.RecordStartsIn(layout.ProxyLog()))

	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure = %v, want the proxy that stopped answering replaced", err)
	}

	if err := holder.Wait(); err != nil {
		t.Errorf("the holder exited with %v, want the stop it was asked for", err)
	}
	starts, err := proxylife.LoggedStarts(layout.ProxyLog())
	if err != nil {
		t.Fatal(err)
	}
	want := []proxylife.LoggedStart{{Command: proxylife.Command, Target: addr}}
	if !slices.Equal(starts, want) {
		t.Errorf("starts = %v, want %v", starts, want)
	}
}

func TestEnsureReplacesASupervisedProxyOfOursThatStoppedAnswering(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this device reads no holder on Windows, so no holder is ever proven ours")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv(versionEnv, "dev")
	silent := filepath.Join(dir, "hold-the-port-and-answer-nothing")
	if err := os.WriteFile(silent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(silentEnv, silent)
	layout := proxytest.SandboxLayout(t, dir)
	addr := freeAddr(t)
	exe := procbin.Self(t, "supervised-silent-hold")

	watchdog := exec.Command(exe, proxylife.Command, proxylife.Supervise, "--addr", addr)
	if err := watchdog.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { watchdog.Process.Kill() })
	watchdogExited := make(chan error, 1)
	go func() { watchdogExited <- watchdog.Wait() }()
	waitListening(t, addr)
	// From here on a spawned proxy answers. The one holding the port
	// was spawned before this and does not.
	if err := os.Remove(silent); err != nil {
		t.Fatal(err)
	}

	p := proxylife.For(layout, "dev", exe, addr, nil)
	t.Cleanup(func() {
		p.Stop()
		waitReleased(t, addr)
		waitLogReleased(t, layout)
	})
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure = %v, want the supervised proxy that stopped answering replaced", err)
	}

	if v := p.Observe(); v.Holder != proxylife.HolderOurs {
		t.Errorf("after Ensure: holder=%v reason=%v, want a proxy that answers at %s", v.Holder, v.Reason, addr)
	}
	select {
	case err := <-watchdogExited:
		if err != nil {
			t.Errorf("the watchdog exited with %v, want it gone with the child it was asked to stop", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the watchdog of the replaced proxy is still running, want it gone with its child")
	}
}

// waitListening waits until something accepts connections at addr.
func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing is listening at %s", addr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStopSendsNoDrainToAnUnprovenHolder(t *testing.T) {
	p, im := healthzCopyHolder(t)
	if err := p.Stop(); !errors.Is(err, proxylife.ErrPortOccupied) {
		t.Errorf("Stop = %v, want the undelivered drain explained by the holder's verdict", err)
	}
	if im.Saw(http.MethodPost, apiproxy.DrainPath) {
		t.Error("a drain request reached a holder that never proved it knows the admin token")
	}
	if im.SawHeader(apiproxy.AdminHeader) {
		t.Error("the admin token was sent to a holder that never proved it knows it")
	}
}

func TestFlushRefusesAnUnprovenHolder(t *testing.T) {
	p, im := healthzCopyHolder(t)
	if _, err := p.Flush(true); !errors.Is(err, proxylife.ErrPortOccupied) {
		t.Errorf("Flush = %v, want ErrPortOccupied", err)
	}
	if im.SawHeader(apiproxy.AdminHeader) {
		t.Error("the admin token was sent to a holder that never proved it knows it")
	}
}

// siblingStillPublishing is a holder that leaves the first challenge
// unproven and proves itself from the next one on, the way a sibling
// caught between winning the bind and publishing its admin token
// answers.
func siblingStillPublishing(t *testing.T) (*proxylife.Proxy, *proxytest.Imposter) {
	t.Helper()
	layout := proxytest.SandboxLayout(t, t.TempDir())
	const token = "feedfacefeedfacefeedfacefeedface"
	im := proxytest.StartImposter(t, proxytest.Health{Service: apiproxy.ServiceName, Version: "1.2.3"})
	proxytest.PublishAdminToken(t, layout, im.Addr(), token)
	im.ProveAfter(1, token)
	return proxylife.For(layout, "1.2.3", "unused", im.Addr(), nil), im
}

func TestFlushWaitsOutASiblingStillPublishingItsAdminToken(t *testing.T) {
	p, im := siblingStillPublishing(t)

	if _, err := p.Flush(true); errors.Is(err, proxylife.ErrPortOccupied) {
		t.Errorf("Flush = %v inside a sibling's startup window, want the holder waited out", err)
	}
	if !im.Saw(http.MethodPost, upload.FlushPath) {
		t.Error("no flush reached a holder that proved itself a moment later")
	}
}

func TestStopWaitsOutASiblingStillPublishingItsAdminToken(t *testing.T) {
	p, im := siblingStillPublishing(t)

	if err := p.Stop(); errors.Is(err, proxylife.ErrPortOccupied) {
		t.Errorf("Stop = %v inside a sibling's startup window, want the holder waited out", err)
	}
	if !im.Saw(http.MethodPost, apiproxy.DrainPath) {
		t.Error("no drain reached a holder that proved itself a moment later")
	}
}

func TestEachManagementRequestOpensItsOwnConnection(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	const token = "feedfacefeedfacefeedfacefeedface"
	im := proxytest.StartImposter(t, proxytest.Health{Service: apiproxy.ServiceName, Version: "1.2.3"})
	proxytest.PublishAdminToken(t, layout, im.Addr(), token)
	im.ProveAfter(0, token)

	p := proxylife.For(layout, "1.2.3", "unused", im.Addr(), nil)
	for range 2 {
		if v := p.Observe(); v.Holder != proxylife.HolderOurs {
			t.Fatalf("holder = %v (%v), want the proven holder", v.Holder, v.Reason)
		}
	}
	if got, want := im.Connections(), im.Requests(); got != want {
		t.Errorf("%d management requests rode %d connections; a connection pooled across a takeover of the port hands the next request a dead one", want, got)
	}
}

// wedgedHolder accepts requests and answers none of them, so every
// exchange with it runs out its own timeout.
func wedgedHolder(t *testing.T) (string, *int32) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var exchanges int32
	wedged := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&exchanges, 1)
		<-wedged
	})}
	go srv.Serve(l)
	t.Cleanup(func() {
		close(wedged)
		srv.Close()
	})
	return l.Addr().String(), &exchanges
}

func TestASettledVerdictSpendsOneWedgedManagementExchange(t *testing.T) {
	addr, exchanges := wedgedHolder(t)

	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", addr, nil)
	v := p.Settled()
	if v.Holder != proxylife.HolderForeign {
		t.Fatalf("holder = %v, want no trust in a holder that answers nothing", v.Holder)
	}
	if v.Reason == nil || !strings.Contains(v.Reason.Error(), "did not answer") {
		t.Errorf("reason = %v, want the silent holder named", v.Reason)
	}
	if got := atomic.LoadInt32(exchanges); got != 1 {
		t.Errorf("a settled verdict spent %d exchanges on a holder that answers nothing, want 1: one exchange's own timeout outlasts the startup grace", got)
	}
}

func TestObserveBlamesAuthenticationWhenNoAdminTokenIsReadable(t *testing.T) {
	live := proxytest.New(t)
	live.AdminToken()

	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", live.Addr(), nil)
	v := p.Observe()
	if v.Holder != proxylife.HolderForeign {
		t.Fatalf("holder = %v, want no trust while no admin token verifies the answer", v.Holder)
	}
	if !errors.Is(v.Reason, proxylife.ErrProxyUnverified) {
		t.Errorf("reason = %v, want the authentication verdict", v.Reason)
	}
	if errors.Is(v.Reason, proxylife.ErrPortOccupied) {
		t.Errorf("reason = %v, must not call a proof-answering holder a stranger", v.Reason)
	}
	if !strings.Contains(v.Reason.Error(), "no admin token") {
		t.Errorf("reason = %v, want the unreadable admin token named", v.Reason)
	}
}

func TestObserveBlamesAuthenticationWhenNoPublishedTokenMatches(t *testing.T) {
	live := proxytest.New(t)
	live.AdminToken()

	layout := proxytest.SandboxLayout(t, t.TempDir())
	proxytest.PublishAdminToken(t, layout, live.Addr(), "feedfacefeedfacefeedfacefeedface")
	p := proxylife.For(layout, "dev", "unused", live.Addr(), nil)
	v := p.Observe()
	if v.Holder != proxylife.HolderForeign {
		t.Fatalf("holder = %v, want no trust while no published token matches", v.Holder)
	}
	if !errors.Is(v.Reason, proxylife.ErrProxyUnverified) {
		t.Errorf("reason = %v, want the authentication verdict", v.Reason)
	}
	if !strings.Contains(v.Reason.Error(), "matches none") {
		t.Errorf("reason = %v, want the mismatch named", v.Reason)
	}
}

func TestObserveExplainsAHolderThatAnswersNothing(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", l.Addr().String(), nil)
	v := p.Observe()
	if v.Holder != proxylife.HolderForeign {
		t.Fatalf("holder = %v, want no trust in a holder that answers nothing", v.Holder)
	}
	if v.Reason == nil || !strings.Contains(v.Reason.Error(), "did not answer") {
		t.Errorf("reason = %v, want the silent holder named", v.Reason)
	}
	if errors.Is(v.Reason, proxylife.ErrPortOccupied) {
		t.Errorf("reason = %v, must not call a silent holder a stranger", v.Reason)
	}
}

// provenHolder is a listener that answers the admin-token challenge
// from a token genuinely published for its address, so probes judge it
// ours, while its endpoints behave however the test says.
func provenHolder(t *testing.T, layout userdirs.Layout, handle http.HandlerFunc) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	const token = "feedfacefeedfacefeedfacefeedface"
	proxytest.PublishAdminToken(t, layout, addr, token)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if nonce := r.Header.Get(apiproxy.ChallengeHeader); nonce != "" {
			w.Header().Set(apiproxy.ProofHeader, apiproxy.Proof(token, nonce, r.Host))
		}
		handle(w, r)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return addr
}

func TestObserveNamesTheProxyOnAnUnreadableHealthAnswer(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	addr := provenHolder(t, layout, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not json")
	})

	p := proxylife.For(layout, "dev", "unused", addr, nil)
	v := p.Observe()
	if v.Holder != proxylife.HolderForeign {
		t.Fatalf("holder = %v, want no trusted self-report out of an unreadable answer", v.Holder)
	}
	if v.Reason == nil || !strings.Contains(v.Reason.Error(), "proxy at "+addr) || !strings.Contains(v.Reason.Error(), "unreadable") {
		t.Errorf("reason = %v, want the proxy named alongside the unreadable answer", v.Reason)
	}
}

func TestFlushNamesTheProxyOnAnUnreadableFlushReply(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	live := proxytest.New(t, proxytest.WithLayout(layout),
		proxytest.WithInternal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "not json")
		})))
	live.AdminToken()

	p := proxylife.For(layout, "1.2.3", "unused", live.Addr(), nil)
	_, err := p.Flush(true)
	if err == nil {
		t.Fatal("Flush decoded an unreadable reply")
	}
	if !strings.Contains(err.Error(), "proxy at "+live.Addr()) || !strings.Contains(err.Error(), "a flush request") {
		t.Errorf("Flush = %v, want the proxy and the flush request named", err)
	}
}

func TestEnsureReportsWhyAnOlderProxyWouldNotDrain(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	health, err := json.Marshal(proxytest.Health{Service: apiproxy.ServiceName, Version: "0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	addr := provenHolder(t, layout, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == apiproxy.DrainPath {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(health)
	})

	p := proxylife.For(layout, "1.0.0", "unused", addr, nil)
	got := p.Ensure()
	if got == nil {
		t.Fatal("Ensure = nil, want the refused drain reported")
	}
	if !strings.Contains(got.Error(), "drain") || !strings.Contains(got.Error(), "500") {
		t.Errorf("Ensure = %v, want the refused drain and its status visible", got)
	}
}

func TestStopExplainsAnUndeliverableDrainWhenNoTokenIsReadable(t *testing.T) {
	live := proxytest.New(t)
	live.AdminToken()

	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", live.Addr(), nil)
	if err := p.Stop(); !errors.Is(err, proxylife.ErrProxyUnverified) {
		t.Errorf("Stop = %v, want the authentication failure reported", err)
	}
}

func TestAReplayedChallengeProofIsRefused(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	live := proxytest.New(t, proxytest.WithLayout(layout))
	live.AdminToken()

	// Any local process may collect proofs from a live proxy for nonces
	// of its own choosing; none of them answers a verifier's fresh nonce.
	req, err := http.NewRequest(http.MethodGet, live.BaseURL()+apiproxy.HealthzPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(apiproxy.ChallengeHeader, "aaaabbbbccccddddaaaabbbbccccdddd")
	resp, err := proxytest.Client(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	collected := resp.Header.Get(apiproxy.ProofHeader)
	if collected == "" {
		t.Fatal("the live proxy answered no proof to collect")
	}

	im := proxytest.StartImposter(t, proxytest.Health{Service: apiproxy.ServiceName, Version: "1.2.3"})
	im.ReplayProof(collected)
	p := proxylife.For(layout, "1.2.3", "unused", im.Addr(), nil)
	if v := p.Observe(); v.Holder != proxylife.HolderForeign {
		t.Errorf("holder = %v for a replayed proof, want foreign", v.Holder)
	}
}

func TestStopDrainsAHolderThatProvesItself(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	live := proxytest.New(t, proxytest.WithLayout(layout))
	live.AdminToken()

	p := proxylife.For(layout, "1.2.3", "unused", live.Addr(), nil)
	p.Stop()
	if err := live.WaitStopped(5 * time.Second); err != nil {
		t.Errorf("Serve = %v after Stop, want a clean drained exit", err)
	}
}

// flushStub answers the flush endpoint with a fixed record count, so a
// test can tell which proxy's mounted endpoint a flush reached.
func flushStub(records int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(upload.FlushReply{Records: records})
	})
}

func TestTwoProxiesOnOneLayoutAreDrivenIndependently(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	a := proxytest.New(t, proxytest.WithLayout(layout), proxytest.WithInternal(flushStub(1)))
	b := proxytest.New(t, proxytest.WithLayout(layout), proxytest.WithInternal(flushStub(2)))
	pa := proxylife.For(layout, "1.2.3", "unused", a.Addr(), nil)
	pb := proxylife.For(layout, "1.2.3", "unused", b.Addr(), nil)

	if v := pa.Observe(); v.Holder != proxylife.HolderOurs {
		t.Fatalf("first proxy holder = %v, want ours", v.Holder)
	}
	if v := pb.Observe(); v.Holder != proxylife.HolderOurs {
		t.Fatalf("second proxy holder = %v, want ours", v.Holder)
	}
	if reply, err := pa.Flush(false); err != nil || reply.Records != 1 {
		t.Errorf("first proxy flush = %+v, %v, want its own mounted endpoint answering", reply, err)
	}
	if reply, err := pb.Flush(false); err != nil || reply.Records != 2 {
		t.Errorf("second proxy flush = %+v, %v, want its own mounted endpoint answering", reply, err)
	}

	pb.Stop()
	if err := b.WaitStopped(5 * time.Second); err != nil {
		t.Fatalf("Serve = %v after Stop", err)
	}
	if v := pb.Observe(); v.Holder != proxylife.HolderNone {
		t.Errorf("stopped proxy holder = %v, want none", v.Holder)
	}
	if v := pa.Observe(); v.Holder != proxylife.HolderOurs {
		t.Errorf("surviving proxy holder = %v after its sibling exited, want ours", v.Holder)
	}
	if reply, err := pa.Flush(false); err != nil || reply.Records != 1 {
		t.Errorf("surviving proxy flush = %+v, %v, want it still reachable", reply, err)
	}
}

func TestRepeatedTakeoversAlwaysLeaveAProvableHolder(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	addr := freeAddr(t)
	p := proxylife.For(layout, "dev", "unused", addr, nil)

	current := proxytest.New(t, proxytest.WithLayout(layout), proxytest.WithAddr(addr), proxytest.WithVersion("0.0.1"))
	for round := 2; round <= 4; round++ {
		if v := p.Observe(); v.Holder != proxylife.HolderOurs {
			t.Fatalf("holder = %v before takeover round %d, want ours", v.Holder, round)
		}
		p.Stop()
		if err := current.WaitStopped(5 * time.Second); err != nil {
			t.Fatalf("Serve = %v on takeover round %d", err, round)
		}
		current = proxytest.New(t, proxytest.WithLayout(layout), proxytest.WithAddr(addr),
			proxytest.WithVersion(fmt.Sprintf("0.0.%d", round)))
	}
	v := p.Observe()
	if v.Holder != proxylife.HolderOurs || v.Health.Version != "0.0.4" {
		t.Errorf("after repeated takeovers: holder=%v health=%+v, want the last instance provable", v.Holder, v.Health)
	}
}

// startFixedNameProxy stands in for a proxy from before per-address
// publication: its admin token lives under the fixed file name, and it
// proves challenges and authorizes requests from that token.
func startFixedNameProxy(t *testing.T, token string) (string, *int32) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var drains int32
	body, err := json.Marshal(proxytest.Health{Service: apiproxy.ServiceName, Version: "0.9.0"})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if nonce := r.Header.Get(apiproxy.ChallengeHeader); nonce != "" {
			w.Header().Set(apiproxy.ProofHeader, apiproxy.Proof(token, nonce, r.Host))
		}
		if r.Header.Get(apiproxy.AdminHeader) != token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == apiproxy.HealthzPath:
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
		case r.Method == http.MethodPost && r.URL.Path == apiproxy.DrainPath:
			atomic.AddInt32(&drains, 1)
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return l.Addr().String(), &drains
}

func TestStopDrainsAProxyPublishedUnderTheFixedName(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	const token = "feedfacefeedfacefeedfacefeedface"
	proxytest.PublishLegacyAdminToken(t, layout, token)
	addr, drains := startFixedNameProxy(t, token)

	p := proxylife.For(layout, "dev", "unused", addr, nil)
	v := p.Observe()
	if v.Holder != proxylife.HolderOurs || v.Health.Version != "0.9.0" {
		t.Fatalf("holder=%v health=%+v, want the fixed-name publication to prove the holder", v.Holder, v.Health)
	}
	p.Stop()
	if atomic.LoadInt32(drains) == 0 {
		t.Error("no authorized drain reached a holder proven through the fixed-name publication")
	}
}

func TestObserveReportsNothingRunning(t *testing.T) {
	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", freeAddr(t), nil)
	if v := p.Observe(); v.Holder != proxylife.HolderNone {
		t.Error("Observe reports a listener on a closed port")
	}
}

func TestStopOnNothingListeningIsANoop(t *testing.T) {
	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", freeAddr(t), nil)
	if err := p.Stop(); err != nil {
		t.Errorf("Stop = %v, want nil: nothing listening is the goal state", err)
	}
	if err := p.Stop(); err != nil {
		t.Errorf("second Stop = %v, want nil", err)
	}
}

// The supervisor is exercised through Supervise, with the spawned child
// standing in for the proxy: the watchdog cannot tell them apart.
func superviseWith(t *testing.T, behavior string) *proxylife.Proxy {
	t.Helper()
	return proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", procbin.Self(t, behavior), freeAddr(t), nil)
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

// TestSuperviseAsksTheChildToStopBeforeKillingIt pins the fix for the
// watchdog's SIGKILL-on-cancel. exec.CommandContext kills by default,
// and the child is where every graceful-exit guarantee lives: the
// uploader's final flush, the drain of in-flight requests, and the
// record queue's finished-but-unwritten captures. An ordinary reboot or
// logout TERMs the watchdog, so this path is the normal way the proxy
// stops, not a corner.
func TestSuperviseAsksTheChildToStopBeforeKillingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no catchable termination signal to send")
	}
	marker := filepath.Join(t.TempDir(), "asked-to-stop")
	t.Setenv(markerEnv, marker)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	if err := superviseWith(t, "graceful-stop").Supervise(ctx, 0, os.Stdout, os.Stderr); err == nil {
		t.Error("Supervise = nil, want the context error")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("the child was killed outright: it never saw a signal it could act on, so its drain and final flush never ran")
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
	p := proxylife.For(layout, "test", "/nonexistent/trajector", addr, nil)
	_, err = p.Selfcheck(token)
	if err == nil {
		t.Fatal("selfcheck against a dead address unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("selfcheck error leaked the token: %v", err)
	}
}

func TestARecordingStarterWritesTheStartItWasAskedForAndStartsNoProcess(t *testing.T) {
	log := filepath.Join(t.TempDir(), "proxy.log")
	projectDir := filepath.Join(t.TempDir(), "a project")

	pid, err := proxylife.RecordStartsIn(log)("/nonexistent/trajector", []string{"hook", "read", projectDir}, "")
	if err != nil {
		t.Fatal(err)
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0", pid)
	}

	got, err := proxylife.LoggedStarts(log)
	if err != nil {
		t.Fatal(err)
	}
	want := []proxylife.LoggedStart{{Command: "hook", Target: projectDir}}
	if !slices.Equal(got, want) {
		t.Errorf("logged starts = %v, want %v", got, want)
	}
}

func TestALogNothingRecordedIntoHasNoStarts(t *testing.T) {
	got, err := proxylife.LoggedStarts(filepath.Join(t.TempDir(), "proxy.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("logged starts = %v, want none", got)
	}
}

func TestEnsureAnsweredByARecordedStartLeavesThePortFree(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	addr := proxytest.IdleAddr(t)
	p := proxylife.For(layout, "dev", "/nonexistent/trajector", addr, proxylife.RecordStartsIn(layout.ProxyLog()))

	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure() = %v, want the recorded start to stand in for a listening proxy", err)
	}

	got, err := proxylife.LoggedStarts(layout.ProxyLog())
	if err != nil {
		t.Fatal(err)
	}
	want := []proxylife.LoggedStart{{Command: proxylife.Command, Target: addr}}
	if !slices.Equal(got, want) {
		t.Errorf("logged starts = %v, want %v", got, want)
	}
	if conn, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
		conn.Close()
		t.Errorf("something listens at %s, want no proxy started", addr)
	}
}
