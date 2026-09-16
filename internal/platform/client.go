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
	// The widest budget any caller may ask for, not requestTimeout, so
	// this is never the binding constraint — http.Client.Timeout is, and
	// it bounds the whole exchange including the body.
	//
	// UploadBatch bounds one attempt by copying this client and setting
	// Timeout to a budget that doubles per consecutive timeout, up to
	// maxUploadBudget. The copy is shallow and shares this transport, so
	// a 30s header timeout here cut every attempt off at 30 seconds
	// however far the budget had escalated: the abort read back as a
	// timeout, the budget doubled, and the retry repeated the attempt
	// that had just failed — forever, with the batch pinned at the head
	// of the queue until the spool filled and recording stopped. That is
	// the failure UploadBudget exists to prevent. 2026-09-16.
	transport.ResponseHeaderTimeout = maxUploadBudget
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
