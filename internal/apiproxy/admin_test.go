package apiproxy_test

import (
	"net/http"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

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
