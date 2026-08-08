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
)

// freshClient keeps a test's connections out of the process-wide pool,
// which outlives the ephemeral addresses these proxies listen on.
func freshClient(t *testing.T) *http.Client {
	t.Helper()
	client := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

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
	resp := challenge(t, freshClient(t), e.BaseURL()+apiproxy.HealthzPath, "", nonce)
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
	resp := challenge(t, freshClient(t), "http://"+e.Addr()+apiproxy.HealthzPath, alias, nonce)
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
			resp, err := http.DefaultClient.Do(r)
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

func TestAdminTokenFileLivesAndDiesWithTheProxy(t *testing.T) {
	e := proxytest.New(t)
	path := e.Layout().AdminTokenFile()

	token := e.AdminToken()
	if len(token) != 32 {
		t.Errorf("published token %q, want 128 bits of hex", token)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
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
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the admin token file outlived the proxy")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
