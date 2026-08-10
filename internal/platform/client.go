package platform

import (
	"net/http"
	"time"
)

// requestTimeout bounds a whole service call including its response
// body. Service calls are never unbounded.
const requestTimeout = 30 * time.Second

// UserAgent is how a trajector build names itself to any HTTP service
// it calls — this service client, and the release source the upgrade
// command reads. One spelling, so every service sees one client name.
func UserAgent(version string) string { return "trajector/" + version }

// newClient builds the HTTP client used for service calls. The
// forwarding proxy never uses it; forwarding has its own transport.
func newClient(version string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{
		Timeout:   requestTimeout,
		Transport: userAgentTransport{agent: UserAgent(version), next: transport},
	}
}

// userAgentTransport identifies every service call as this trajector
// build.
type userAgentTransport struct {
	agent string
	next  http.RoundTripper
}

func (t userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", t.agent)
	return t.next.RoundTrip(req)
}
