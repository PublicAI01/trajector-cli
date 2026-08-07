package apiproxy_test

import (
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestForeignHostHeaderIsRejectedOnEveryPath(t *testing.T) {
	e := proxytest.New(t)
	e.WriteTable(activeTable("tok1", e.Upstream.URL()))

	for _, path := range []string{"/t/tok1/v1/messages", apiproxy.HealthzPath, apiproxy.DrainPath} {
		req, err := http.NewRequest(http.MethodPost, e.BaseURL()+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "evil.example:41100"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s with a foreign Host = %d, want 404", path, resp.StatusCode)
		}
	}
	if n := len(e.Upstream.Requests()); n != 0 {
		t.Errorf("upstream saw %d requests, want none forwarded under a foreign Host", n)
	}
	// The rejected drain must not have stopped the proxy.
	if h := e.Healthz(); h.Service != apiproxy.ServiceName {
		t.Errorf("healthz service = %q after a rejected drain", h.Service)
	}
}

func TestLoopbackAliasHostsAreAccepted(t *testing.T) {
	e := proxytest.New(t)
	_, port, err := net.SplitHostPort(e.Addr())
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"localhost:" + port, "127.0.0.1:" + port} {
		req, err := http.NewRequest(http.MethodGet, e.BaseURL()+apiproxy.HealthzPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		req.Header.Set(apiproxy.AdminHeader, e.AdminToken())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("healthz with Host %q = %d, want 200", host, resp.StatusCode)
		}
	}
}
