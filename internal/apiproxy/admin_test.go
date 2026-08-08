package apiproxy_test

import (
	"net"
	"net/http"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

func challenge(t *testing.T, client *http.Client, url, host, nonce string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	req.Header.Set(apiproxy.ChallengeHeader, nonce)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestHealthzAnswersAChallengeWithoutTheAdminToken(t *testing.T) {
	e := proxytest.New(t)
	token := e.AdminToken()

	const nonce = "00112233445566778899aabbccddeeff"
	resp := challenge(t, proxytest.Client(t), e.BaseURL()+apiproxy.HealthzPath, "", nonce)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("healthz with a challenge and no token = %d, want 401 with the counters withheld", resp.StatusCode)
	}
	if got, want := resp.Header.Get(apiproxy.ProofHeader), apiproxy.Proof(token, nonce, e.Addr()); got != want {
		t.Errorf("proof = %q, want the answer for the published token over this nonce and host", got)
	}
}

func TestChallengeProofIsBoundToTheRequestedHost(t *testing.T) {
	e := proxytest.New(t)
	token := e.AdminToken()
	_, port, err := net.SplitHostPort(e.Addr())
	if err != nil {
		t.Fatal(err)
	}
	alias := net.JoinHostPort("localhost", port)

	const nonce = "ffeeddccbbaa99887766554433221100"
	resp := challenge(t, proxytest.Client(t), "http://"+e.Addr()+apiproxy.HealthzPath, alias, nonce)
	got := resp.Header.Get(apiproxy.ProofHeader)
	if got != apiproxy.Proof(token, nonce, alias) {
		t.Errorf("proof = %q, want the answer for the host the caller addressed", got)
	}
	if got == apiproxy.Proof(token, nonce, e.Addr()) {
		t.Error("a proof answered for one host verifies for another")
	}
}

func TestReservedEndpointsAnswer401WithoutTheAdminToken(t *testing.T) {
	e := proxytest.New(t)

	for name, req := range map[string]func() *http.Request{
		"missing": func() *http.Request {
			req, _ := http.NewRequest(http.MethodPost, e.BaseURL()+apiproxy.DrainPath, nil)
			return req
		},
		"wrong": func() *http.Request {
			req, _ := http.NewRequest(http.MethodPost, e.BaseURL()+apiproxy.DrainPath, nil)
			req.Header.Set(apiproxy.AdminHeader, "0000feed0000feed0000feed0000feed")
			return req
		},
	} {
		for _, path := range []string{apiproxy.DrainPath, apiproxy.HealthzPath, "/trajector/flush"} {
			r := req()
			r.URL.Path = path
			resp, err := e.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s with %s token = %d, want 401", path, name, resp.StatusCode)
			}
		}
	}
}

func TestBrowserDriveByCannotDrainOrFlush(t *testing.T) {
	flushed := 0
	e := proxytest.New(t, proxytest.WithInternal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flushed++
	})))

	// A browser's cross-origin POST arrives with the right Host and no
	// way to read the user's token file.
	for _, path := range []string{apiproxy.DrainPath, "/trajector/flush"} {
		resp := e.Post(path, "", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", path, resp.StatusCode)
		}
	}
	if flushed != 0 {
		t.Errorf("the mounted flush handler ran %d time(s) without the admin token", flushed)
	}
	if h := e.Healthz(); h.Service != apiproxy.ServiceName {
		t.Errorf("healthz = %+v after a refused drain, want the proxy still up", h)
	}
}

// existingPublications reports which admin-token candidates for addr
// are present on disk right now.
func existingPublications(t *testing.T, layout userdirs.Layout, addr string) []string {
	t.Helper()
	var files []string
	for _, path := range layout.AdminTokenCandidates(addr) {
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
		}
	}
	return files
}

func waitNoPublications(t *testing.T, layout userdirs.Layout, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		files := existingPublications(t, layout, addr)
		if len(files) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("admin token publications outlived the proxy: %v", files)
		}
		time.Sleep(10 * time.Millisecond)
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

func TestAdminTokenFileLivesAndDiesWithTheProxy(t *testing.T) {
	e := proxytest.New(t)

	token := e.AdminToken()
	if len(token) != 32 {
		t.Errorf("published token %q, want 128 bits of hex", token)
	}
	files := existingPublications(t, e.Layout(), e.Addr())
	if len(files) != 1 {
		t.Fatalf("publications = %v, want exactly one for the serving proxy", files)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(files[0])
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("token file mode = %v, want 0600", info.Mode().Perm())
		}
	}

	resp := e.PostAdmin(apiproxy.DrainPath)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("drain = %d", resp.StatusCode)
	}
	if err := e.WaitStopped(5 * time.Second); err != nil {
		t.Fatalf("Serve = %v", err)
	}
	waitNoPublications(t, e.Layout(), e.Addr())
}

func TestProxiesOnTwoAddressesPublishAndExitIndependently(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	a := proxytest.New(t, proxytest.WithLayout(layout))
	b := proxytest.New(t, proxytest.WithLayout(layout))

	tokenA := a.AdminToken()
	if tokenB := b.AdminToken(); tokenB == tokenA {
		t.Fatal("two proxies published one token; their publications must be independent")
	}
	a.Healthz()
	b.Healthz()

	if resp := b.PostAdmin(apiproxy.DrainPath); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("drain = %d", resp.StatusCode)
	}
	if err := b.WaitStopped(5 * time.Second); err != nil {
		t.Fatalf("Serve = %v", err)
	}
	waitNoPublications(t, layout, b.Addr())

	if got := a.AdminToken(); got != tokenA {
		t.Errorf("first proxy's token = %q after the second exited, want %q untouched", got, tokenA)
	}
	a.Healthz()
}

func TestExitRemovesOnlyTheExitingInstancesPublication(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	e := proxytest.New(t, proxytest.WithLayout(layout))
	token := e.AdminToken()

	const successor = "feedfacefeedfacefeedfacefeedface"
	proxytest.PublishAdminToken(t, layout, e.Addr(), successor)

	req, err := http.NewRequest(http.MethodPost, e.BaseURL()+apiproxy.DrainPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(apiproxy.AdminHeader, token)
	resp, err := e.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("drain = %d", resp.StatusCode)
	}
	if err := e.WaitStopped(5 * time.Second); err != nil {
		t.Fatalf("Serve = %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		files := existingPublications(t, layout, e.Addr())
		if len(files) == 1 {
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != successor {
				t.Fatalf("surviving publication holds %q, want the successor's token", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("publications = %v, want only the successor's to survive the exit", files)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPublishClearsLeftoversForItsAddressOnly(t *testing.T) {
	layout := proxytest.SandboxLayout(t, t.TempDir())
	addr := freeAddr(t)
	otherAddr := freeAddr(t)
	const leftover = "00ff00ff00ff00ff00ff00ff00ff00ff"
	const other = "11aa11aa11aa11aa11aa11aa11aa11aa"
	const fixed = "22bb22bb22bb22bb22bb22bb22bb22bb"
	proxytest.PublishAdminToken(t, layout, addr, leftover)
	proxytest.PublishAdminToken(t, layout, otherAddr, other)
	proxytest.PublishLegacyAdminToken(t, layout, fixed)

	e := proxytest.New(t, proxytest.WithLayout(layout), proxytest.WithAddr(addr))
	e.Healthz()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var stale bool
		for _, path := range existingPublications(t, layout, addr) {
			data, err := os.ReadFile(path)
			if err == nil && string(data) == leftover {
				stale = true
			}
		}
		if !stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the crashed predecessor's leftover publication survived a new publish")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if data, err := os.ReadFile(layout.LegacyAdminTokenFile()); err != nil || string(data) != fixed {
		t.Errorf("fixed-name publication = %q, %v; an older proxy's file is never a publisher's to clear", data, err)
	}
	otherFiles := existingPublications(t, layout, otherAddr)
	if len(otherFiles) != 2 {
		t.Fatalf("publications for the other address = %v, want its own file plus the fixed name", otherFiles)
	}
	if data, err := os.ReadFile(otherFiles[0]); err != nil || string(data) != other {
		t.Errorf("other address's publication = %q, %v, want it untouched", data, err)
	}
}
