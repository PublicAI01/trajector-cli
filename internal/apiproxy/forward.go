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

func (s *Server) newForwarder() http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite:        s.rewrite,
		ModifyResponse: s.modifyResponse,
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
