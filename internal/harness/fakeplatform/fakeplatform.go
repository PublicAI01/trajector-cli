// Package fakeplatform is a scripted stand-in for the trajector service
// API. Upload and pairing seam tests stub endpoints per method and path
// and assert against the recorded requests.
//
// TODO: this package is the convergence target for the three sibling
// batch-ack builders (echoAck in upload, stubEchoAck in cli, ackBatch
// in lifecycle); fold them in the next time ack semantics change.
package fakeplatform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/platform"
)

// Request is one recorded call to the fake service.
type Request struct {
	Method string
	// URL is the full request URI including any query string.
	URL    string
	Header http.Header
	Body   []byte
}

// Response is one scripted response for a stubbed endpoint.
type Response struct {
	// Status defaults to 200.
	Status int
	Header http.Header
	Body   []byte
}

// PairableAs scripts a complete pairing flow that ends paired as
// deviceToken. The bodies are the client's own wire structs, so a
// renamed field breaks here at compile time instead of at decode time
// in every test that spelled the shape by hand.
func (s *Server) PairableAs(pairingID, deviceToken string) {
	s.stubPairingStart(pairingID)
	s.stubPaired(pairingID, deviceToken)
}

// PairableAsAfterOutage scripts a pairing whose first status check
// fails with a service-side error before ending paired as deviceToken.
func (s *Server) PairableAsAfterOutage(pairingID, deviceToken string) {
	s.stubPairingStart(pairingID)
	s.stubPairingOutage(pairingID)
	s.stubPaired(pairingID, deviceToken)
}

// PairingOutage scripts a pairing whose every status check fails with a
// service-side error.
func (s *Server) PairingOutage(pairingID string) {
	s.stubPairingStart(pairingID)
	s.stubPairingOutage(pairingID)
}

// PairingVanishes scripts a pairing the service no longer recognizes.
func (s *Server) PairingVanishes(pairingID string) {
	s.stubPairingStart(pairingID)
	s.Stub("GET", "/v1/pairings/"+pairingID, JSON(404, map[string]any{"error": "unknown pairing"}))
}

// PairingExpires scripts a pairing whose link expires before approval.
func (s *Server) PairingExpires(pairingID string) {
	s.stubPairingStart(pairingID)
	s.Stub("GET", "/v1/pairings/"+pairingID, JSON(200, platform.PairingResult{
		Status: platform.PairingExpired,
	}))
}

func (s *Server) stubPairingStart(pairingID string) {
	s.Stub("POST", "/v1/pairings", JSON(200, platform.Pairing{
		PairingID:       pairingID,
		VerificationURL: "https://example.com/pair/" + pairingID,
		UserCode:        "ABCD-1234",
		PollIntervalMS:  1,
	}))
}

func (s *Server) stubPaired(pairingID, deviceToken string) {
	s.Stub("GET", "/v1/pairings/"+pairingID, JSON(200, platform.PairingResult{
		Status:      platform.PairingPaired,
		DeviceToken: deviceToken,
	}))
}

func (s *Server) stubPairingOutage(pairingID string) {
	s.Stub("GET", "/v1/pairings/"+pairingID, JSON(502, map[string]any{"error": "bad gateway"}))
}

// JSON builds a response with a JSON-encoded body.
func JSON(status int, v any) Response {
	body, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("fakeplatform: encoding stub body: %v", err))
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return Response{Status: status, Header: h, Body: body}
}

// Server is the fake service. Requests to endpoints without a stub fail
// loudly with status 590 so tests never pass against an accidental
// default.
type Server struct {
	HTTP *httptest.Server

	mu       sync.Mutex
	stubs    map[string][]func(Request) Response
	requests []Request
}

// New starts the fake service and shuts it down with the test.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{stubs: map[string][]func(Request) Response{}}
	s.HTTP = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.HTTP.Close)
	return s
}

// URL returns the service base URL.
func (s *Server) URL() string { return s.HTTP.URL }

// Stub queues a response for the given method and path. Responses for
// the same endpoint are consumed in FIFO order; the last one is sticky
// so steady-state endpoints need a single stub.
func (s *Server) Stub(method, path string, resp Response) {
	s.StubFunc(method, path, func(Request) Response { return resp })
}

// StubFunc queues a response computed from the request, for endpoints
// whose reply must echo request content. Consumption order matches
// Stub.
func (s *Server) StubFunc(method, path string, respond func(Request) Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := method + " " + path
	s.stubs[key] = append(s.stubs[key], respond)
}

// Parts decodes a recorded multipart request body by form field name.
func Parts(r Request) (map[string][]byte, error) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, fmt.Errorf("fakeplatform: parsing content type: %w", err)
	}
	if mediaType != "multipart/form-data" {
		return nil, fmt.Errorf("fakeplatform: content type %q is not multipart/form-data", mediaType)
	}
	mr := multipart.NewReader(bytes.NewReader(r.Body), params["boundary"])
	parts := map[string][]byte{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return parts, nil
		}
		if err != nil {
			return nil, fmt.Errorf("fakeplatform: reading multipart: %w", err)
		}
		data, err := io.ReadAll(p)
		if err != nil {
			return nil, fmt.Errorf("fakeplatform: reading part %q: %w", p.FormName(), err)
		}
		parts[p.FormName()] = data
	}
}

// Requests returns a snapshot of everything received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("fakeplatform: reading body: %v", err), 590)
		return
	}
	recorded := Request{
		Method: r.Method,
		URL:    r.URL.RequestURI(),
		Header: r.Header.Clone(),
		Body:   body,
	}
	s.mu.Lock()
	s.requests = append(s.requests, recorded)
	key := r.Method + " " + r.URL.Path
	queue := s.stubs[key]
	var respond func(Request) Response
	found := len(queue) > 0
	if found {
		respond = queue[0]
		if len(queue) > 1 {
			s.stubs[key] = queue[1:]
		}
	}
	s.mu.Unlock()

	if !found {
		http.Error(w, fmt.Sprintf("fakeplatform: no stub for %s", key), 590)
		return
	}
	resp := respond(recorded)
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	w.Write(resp.Body)
}
