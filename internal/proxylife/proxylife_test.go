package proxylife_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
	"github.com/PublicAI01/trajector-cli/internal/upload"
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
	version := os.Getenv(versionEnv)
	if version == "" {
		version = "dev"
	}
	m, err := lifecycle.Open(lifecycle.Deps{
		Layout:    layout,
		Tokens:    tokenstore.Files(layout.SecretsDir()),
		Platform:  platform.New("http://127.0.0.1:1", version),
		Version:   version,
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
	p := proxylife.For(layout, version, procbin.Self(t, "proxy"), addr)
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
	return p, layout
}

func TestEnsureStartsSupervisedProxyAndIsIdempotent(t *testing.T) {
	p, _ := supervised(t, freeAddr(t), "dev")

	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	h, holder := p.Health()
	if holder != proxylife.HolderOurs || h.Version != "dev" {
		t.Fatalf("after Ensure: holder=%v health=%+v", holder, h)
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
	h, holder := p.Health()
	if holder != proxylife.HolderOurs || h.Version != "1.0.0" {
		t.Errorf("after takeover: holder=%v health=%+v, want version 1.0.0", holder, h)
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

	p := proxylife.For(layout, "1.0.0", "unspawnable", addr)
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure = %v, want the newer proxy reused", err)
	}
	if h, holder := p.Health(); holder != proxylife.HolderOurs || h.Version != "2.0.0" {
		t.Errorf("after Ensure: holder=%v version=%q, want the newer proxy left serving", holder, h.Version)
	}
	if log := proxyLogContents(t, layout); !strings.Contains(log, proxylife.ReuseReason) {
		t.Errorf("proxy log = %q, want the reuse decision on record", log)
	}
}

func TestEnsureFromADevBuildReusesAReleaseProxy(t *testing.T) {
	layout, addr := servingHolder(t, "1.2.3")

	p := proxylife.For(layout, "dev", "unspawnable", addr)
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure = %v, want the release proxy reused", err)
	}
	if h, holder := p.Health(); holder != proxylife.HolderOurs || h.Version != "1.2.3" {
		t.Errorf("after Ensure: holder=%v version=%q, want the release proxy left serving", holder, h.Version)
	}
	log := proxyLogContents(t, layout)
	if !strings.Contains(log, "reuses the version 1.2.3 proxy") || !strings.Contains(log, proxylife.ReuseReason) {
		t.Errorf("proxy log = %q, want the reuse decision on record with its reason", log)
	}
}

func TestEnsureFromAReleaseBuildReusesADevProxy(t *testing.T) {
	layout, addr := servingHolder(t, "dev")

	p := proxylife.For(layout, "9.9.9", "unspawnable", addr)
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure = %v, want the dev proxy reused", err)
	}
	if h, holder := p.Health(); holder != proxylife.HolderOurs || h.Version != "dev" {
		t.Errorf("after Ensure: holder=%v version=%q, want the dev proxy left serving", holder, h.Version)
	}
	if log := proxyLogContents(t, layout); !strings.Contains(log, proxylife.ReuseReason) {
		t.Errorf("proxy log = %q, want the reuse decision on record", log)
	}
}

func TestSupersedesOnlyAStrictlyOlderSemanticVersion(t *testing.T) {
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
			if got := proxylife.Supersedes(tc.ours, tc.holder); got != tc.want {
				t.Errorf("Supersedes(%q, %q) = %v, want %v", tc.ours, tc.holder, got, tc.want)
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

	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", l.Addr().String())
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
	return proxylife.For(layout, "dev", "unused", im.Addr()), im
}

func TestHealthTreatsAHealthzCopyAsForeign(t *testing.T) {
	p, im := healthzCopyHolder(t)
	if _, holder := p.Health(); holder != proxylife.HolderForeign {
		t.Errorf("holder = %v for a listener copying the health payload, want foreign", holder)
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

func TestStopSendsNoDrainToAnUnprovenHolder(t *testing.T) {
	p, im := healthzCopyHolder(t)
	p.Stop()
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
	p := proxylife.For(layout, "1.2.3", "unused", im.Addr())
	if _, holder := p.Health(); holder != proxylife.HolderForeign {
		t.Errorf("holder = %v for a replayed proof, want foreign", holder)
	}
}

func TestStopDrainsAHolderThatProvesItself(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	live := proxytest.New(t, proxytest.WithLayout(layout))
	live.AdminToken()

	p := proxylife.For(layout, "1.2.3", "unused", live.Addr())
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
	pa := proxylife.For(layout, "1.2.3", "unused", a.Addr())
	pb := proxylife.For(layout, "1.2.3", "unused", b.Addr())

	if _, holder := pa.Health(); holder != proxylife.HolderOurs {
		t.Fatalf("first proxy holder = %v, want ours", holder)
	}
	if _, holder := pb.Health(); holder != proxylife.HolderOurs {
		t.Fatalf("second proxy holder = %v, want ours", holder)
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
	if _, holder := pb.Health(); holder != proxylife.HolderNone {
		t.Errorf("stopped proxy holder = %v, want none", holder)
	}
	if _, holder := pa.Health(); holder != proxylife.HolderOurs {
		t.Errorf("surviving proxy holder = %v after its sibling exited, want ours", holder)
	}
	if reply, err := pa.Flush(false); err != nil || reply.Records != 1 {
		t.Errorf("surviving proxy flush = %+v, %v, want it still reachable", reply, err)
	}
}

func TestRepeatedTakeoversAlwaysLeaveAProvableHolder(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	addr := freeAddr(t)
	p := proxylife.For(layout, "dev", "unused", addr)

	current := proxytest.New(t, proxytest.WithLayout(layout), proxytest.WithAddr(addr), proxytest.WithVersion("0.0.1"))
	for round := 2; round <= 4; round++ {
		if _, holder := p.Health(); holder != proxylife.HolderOurs {
			t.Fatalf("holder = %v before takeover round %d, want ours", holder, round)
		}
		p.Stop()
		if err := current.WaitStopped(5 * time.Second); err != nil {
			t.Fatalf("Serve = %v on takeover round %d", err, round)
		}
		current = proxytest.New(t, proxytest.WithLayout(layout), proxytest.WithAddr(addr),
			proxytest.WithVersion(fmt.Sprintf("0.0.%d", round)))
	}
	h, holder := p.Health()
	if holder != proxylife.HolderOurs || h.Version != "0.0.4" {
		t.Errorf("after repeated takeovers: holder=%v health=%+v, want the last instance provable", holder, h)
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

	p := proxylife.For(layout, "dev", "unused", addr)
	h, holder := p.Health()
	if holder != proxylife.HolderOurs || h.Version != "0.9.0" {
		t.Fatalf("holder=%v health=%+v, want the fixed-name publication to prove the holder", holder, h)
	}
	p.Stop()
	if atomic.LoadInt32(drains) == 0 {
		t.Error("no authorized drain reached a holder proven through the fixed-name publication")
	}
}

func TestHealthReportsNothingRunning(t *testing.T) {
	p := proxylife.For(proxytest.SandboxLayout(t, t.TempDir()), "dev", "unused", freeAddr(t))
	if _, holder := p.Health(); holder != proxylife.HolderNone {
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
