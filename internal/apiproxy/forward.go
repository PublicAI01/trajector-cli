package apiproxy

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// tokenPrefix is the injected base-URL path prefix carrying the
// project consent token: /t/<token>/<upstream path>.
const tokenPrefix = "/t/"

type decisionKey struct{}

// decision is the per-request routing verdict, made once before
// forwarding and carried through the exchange via the request context.
// Everything about recording lives behind rec, which is nil whenever
// this exchange is only forwarded.
type decision struct {
	upstream *url.URL
	restPath string
	rec      *recorder
}

// newTransport is the forwarding path's own connection pool. Left nil,
// a ReverseProxy borrows http.DefaultTransport, which would put the
// user's API traffic in the same pool as this tool's own service calls:
// a burst of uploads could then evict a connection a forwarded request
// was about to reuse, and recording would be observable as latency.
// The per-host idle limit comes off its default of two because a proxy
// spends its whole life talking to one host, and a session running more
// concurrent requests than that would reconnect for every one.
func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = t.MaxIdleConns
	return t
}

func (s *Server) newForwarder() http.Handler {
	s.transport = newTransport()
	proxy := &httputil.ReverseProxy{
		Rewrite:        s.rewrite,
		ModifyResponse: s.modifyResponse,
		Transport:      s.transport,
		// Stream responses through unbuffered so the client observes
		// upstream bytes as they arrive.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.cfg.Logf("upstream exchange failed: %v", err)
			w.WriteHeader(http.StatusBadGateway)
		},
		ErrorLog: nil,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Full duplex, or the server guards the request body against a
		// handler that responds before reading it: writing the upstream's
		// header while the body looks unconsumed makes the server drain
		// and close that body, racing the transport's own trailing read
		// of it. Losing that race tears down the upstream connection in
		// the middle of the response, so the client sees a truncated
		// exchange the upstream completed. A proxy is the case the guard
		// exists for the opposite of: both directions are supposed to
		// stream at once. Only HTTP/2 refuses, where full duplex is
		// already the rule, so the error is discarded.
		http.NewResponseController(w).EnableFullDuplex()
		d := s.decide(r)
		d.rec.observeRequest(r)
		ctx := context.WithValue(r.Context(), decisionKey{}, d)
		proxy.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) decide(r *http.Request) *decision {
	d := &decision{restPath: r.URL.Path}
	upstream := s.cfg.DefaultUpstream
	var route routing.Route
	record := false

	if token, rest, ok := splitToken(r.URL.Path); ok {
		d.restPath = rest
		found, verdict := s.cfg.Table.Lookup(token)
		if verdict.Resolves() {
			route = found
			upstream = found.Upstream
		}
		if verdict.Records() {
			s.touchAuthorized()
			record = s.cfg.Dialect.ShouldRecord(r.Method, rest)
		}
	}

	u, err := url.Parse(upstream)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		// One unusable route upstream is treated exactly like a table
		// that cannot be read at all: forward at the default upstream
		// and record nothing. A bad table entry must not cost the user
		// their traffic.
		s.stats.countUnusableUpstream()
		u, _ = url.Parse(s.cfg.DefaultUpstream)
		record = false
	}
	d.upstream = u
	if record {
		d.rec = s.newRecorder(route, d.restPath, envelope.FormatHints{
			AnthropicVersion: r.Header.Get("anthropic-version"),
			AnthropicBeta:    splitBetaHeader(r.Header.Values("anthropic-beta")),
		})
	}
	return d
}

// splitToken extracts the consent token from /t/<token>/<rest>.
func splitToken(path string) (token, rest string, ok bool) {
	if !strings.HasPrefix(path, tokenPrefix) {
		return "", "", false
	}
	token, rest, _ = strings.Cut(path[len(tokenPrefix):], "/")
	if token == "" {
		return "", "", false
	}
	return token, "/" + rest, true
}

func splitBetaHeader(values []string) []string {
	var betas []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				betas = append(betas, part)
			}
		}
	}
	return betas
}

func (s *Server) rewrite(pr *httputil.ProxyRequest) {
	d, _ := pr.In.Context().Value(decisionKey{}).(*decision)
	target := *d.upstream
	target.Path = strings.TrimSuffix(d.upstream.Path, "/") + d.restPath
	target.RawQuery = pr.In.URL.RawQuery
	pr.Out.URL = &target
	pr.Out.Host = ""
	// Every forwarded request drops Accept-Encoding, recorded or not, so
	// the transport negotiates a coding it can decode transparently.
	// Normalizing unconditionally is what makes recording unobservable:
	// a header that varied with consent state would let the exchange
	// itself reveal whether it was being watched.
	pr.Out.Header.Del("Accept-Encoding")
}

// modifyResponse always returns nil. A non-nil error here becomes a 502
// in place of a response the client is entitled to, so nothing on the
// recording path may report failure through it; the recorder reports
// its own failures to itself.
func (s *Server) modifyResponse(resp *http.Response) error {
	d, _ := resp.Request.Context().Value(decisionKey{}).(*decision)
	if d != nil {
		d.rec.observeResponse(resp)
	}
	return nil
}
