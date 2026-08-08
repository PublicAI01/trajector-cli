package fakeupstream_test

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
)

func TestRecordsRequestVerbatim(t *testing.T) {
	s := fakeupstream.New(t)
	s.Enqueue(fakeupstream.Response{Body: []byte(`{"ok":true}`)})

	body := `{"model":"claude-fake","messages":[]}`
	req, err := http.NewRequest(http.MethodPost, s.URL()+"/v1/messages?beta=true", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", "sk-test-fake")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp, err := s.HTTP.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	reqs := s.Requests()
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Method != http.MethodPost || got.URL != "/v1/messages?beta=true" {
		t.Errorf("recorded %s %s, want POST /v1/messages?beta=true", got.Method, got.URL)
	}
	if !bytes.Equal(got.Body, []byte(body)) {
		t.Errorf("recorded body %q, want %q", got.Body, body)
	}
	if got.Header.Get("X-Api-Key") != "sk-test-fake" {
		t.Errorf("recorded X-Api-Key = %q, want sk-test-fake", got.Header.Get("X-Api-Key"))
	}
}

func TestServesScriptedResponsesInOrder(t *testing.T) {
	s := fakeupstream.New(t)
	s.Enqueue(fakeupstream.Response{Status: 200, Body: []byte("first")})
	s.Enqueue(fakeupstream.Response{Status: 529, Body: []byte("overloaded")})

	for i, want := range []struct {
		status int
		body   string
	}{{200, "first"}, {529, "overloaded"}} {
		resp, err := s.HTTP.Client().Post(s.URL(), "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want.status || buf.String() != want.body {
			t.Errorf("response %d = %d %q, want %d %q", i, resp.StatusCode, buf.String(), want.status, want.body)
		}
	}
}

func TestUnscriptedRequestFailsLoudly(t *testing.T) {
	s := fakeupstream.New(t)
	resp, err := s.HTTP.Client().Get(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 590 {
		t.Errorf("status = %d, want 590", resp.StatusCode)
	}
}

func TestStreamsSSEBlocksVerbatim(t *testing.T) {
	s := fakeupstream.New(t)
	blocks := []string{
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
		"event: content_block_delta\ndata: {\"delta\":{\"text\":\"hi\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	s.Enqueue(fakeupstream.Response{SSE: blocks})

	resp, err := s.HTTP.Client().Get(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(bufio.NewReader(resp.Body)); err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(blocks, ""); buf.String() != want {
		t.Errorf("stream = %q, want %q", buf.String(), want)
	}
}

func TestResponseHeadersWrittenVerbatim(t *testing.T) {
	s := fakeupstream.New(t)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Request-Id", "req_fake")
	s.Enqueue(fakeupstream.Response{Header: h, Body: []byte("{}")})

	resp, err := s.HTTP.Client().Get(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Request-Id"); got != "req_fake" {
		t.Errorf("Request-Id = %q, want req_fake", got)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}
