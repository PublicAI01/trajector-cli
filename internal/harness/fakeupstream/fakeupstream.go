// Package fakeupstream is a scripted stand-in for an Anthropic-style
// API upstream. It records every request verbatim so proxy seam tests
// can assert byte-level forwarding equivalence, and it serves scripted
// responses including SSE streams, malformed payloads, and failures.
package fakeupstream

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Request is one recorded upstream request.
type Request struct {
	Method string
	// URL is the full request URI including any query string.
	URL    string
	Header http.Header
	// Body holds the request body exactly as received.
	Body []byte
}

// Response is one scripted upstream response, consumed in FIFO order.
type Response struct {
	// Status defaults to 200.
	Status int
	// Header entries are written verbatim before the body.
	Header http.Header
	// Body is served when SSE is empty.
	Body []byte
	// SSE blocks are each written verbatim and flushed individually so
	// clients observe incremental delivery.
	SSE []string
	// Delay is applied before the body and before each SSE block.
	Delay time.Duration
}

// Server is the fake upstream. Unscripted requests fail loudly with
// status 590 so tests never pass against an accidental default.
type Server struct {
	HTTP *httptest.Server

	mu       sync.Mutex
	queue    []Response
	requests []Request
}

// New starts the fake upstream and shuts it down with the test.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{}
	s.HTTP = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.HTTP.Close)
	return s
}

// URL returns the upstream base URL.
func (s *Server) URL() string { return s.HTTP.URL }

// Enqueue scripts the response for the next request.
func (s *Server) Enqueue(r Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, r)
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
		http.Error(w, fmt.Sprintf("fakeupstream: reading body: %v", err), 590)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method: r.Method,
		URL:    r.URL.RequestURI(),
		Header: r.Header.Clone(),
		Body:   body,
	})
	var next Response
	scripted := len(s.queue) > 0
	if scripted {
		next = s.queue[0]
		s.queue = s.queue[1:]
	}
	s.mu.Unlock()

	if !scripted {
		http.Error(w, "fakeupstream: no scripted response", 590)
		return
	}
	writeResponse(w, next)
}

func writeResponse(w http.ResponseWriter, resp Response) {
	header := w.Header()
	// Suppress automatic content-type sniffing; seam tests assert
	// headers verbatim.
	header["Content-Type"] = nil
	if len(resp.SSE) > 0 && resp.Header.Get("Content-Type") == "" {
		header.Set("Content-Type", "text/event-stream")
	}
	for k, vs := range resp.Header {
		header[k] = vs
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)

	if len(resp.SSE) == 0 {
		time.Sleep(resp.Delay)
		w.Write(resp.Body)
		return
	}
	flusher, _ := w.(http.Flusher)
	for _, block := range resp.SSE {
		time.Sleep(resp.Delay)
		io.WriteString(w, block)
		if flusher != nil {
			flusher.Flush()
		}
	}
}
