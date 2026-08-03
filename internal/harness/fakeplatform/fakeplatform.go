// Package fakeplatform is a scripted stand-in for the trajector service
// API. Upload and pairing seam tests stub endpoints per method and path
// and assert against the recorded requests.
package fakeplatform

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
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
	stubs    map[string][]Response
	requests []Request
}

// New starts the fake service and shuts it down with the test.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{stubs: map[string][]Response{}}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	key := method + " " + path
	s.stubs[key] = append(s.stubs[key], resp)
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
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method: r.Method,
		URL:    r.URL.RequestURI(),
		Header: r.Header.Clone(),
		Body:   body,
	})
	key := r.Method + " " + r.URL.Path
	queue := s.stubs[key]
	var resp Response
	found := len(queue) > 0
	if found {
		resp = queue[0]
		if len(queue) > 1 {
			s.stubs[key] = queue[1:]
		}
	}
	s.mu.Unlock()

	if !found {
		http.Error(w, fmt.Sprintf("fakeplatform: no stub for %s", key), 590)
		return
	}
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
