package fakeplatform_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
)

func TestStubbedEndpointServesJSON(t *testing.T) {
	s := fakeplatform.New(t)
	s.Stub("POST", "/v1/batches", fakeplatform.JSON(200, map[string]any{"ack": true}))

	resp, err := s.HTTP.Client().Post(s.URL()+"/v1/batches", "application/octet-stream", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct{ Ack bool }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !body.Ack {
		t.Errorf("response = %d ack=%v, want 200 ack=true", resp.StatusCode, body.Ack)
	}
}

func TestStubsConsumedFIFOWithStickyLast(t *testing.T) {
	s := fakeplatform.New(t)
	s.Stub("GET", "/v1/handshake", fakeplatform.JSON(500, map[string]string{"error": "transient"}))
	s.Stub("GET", "/v1/handshake", fakeplatform.JSON(200, map[string]string{"status": "ok"}))

	wantStatuses := []int{500, 200, 200}
	for i, want := range wantStatuses {
		resp, err := s.HTTP.Client().Get(s.URL() + "/v1/handshake")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("call %d status = %d, want %d", i, resp.StatusCode, want)
		}
	}
}

func TestUnstubbedEndpointFailsLoudly(t *testing.T) {
	s := fakeplatform.New(t)
	resp, err := s.HTTP.Client().Get(s.URL() + "/v1/unknown")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 590 {
		t.Errorf("status = %d, want 590", resp.StatusCode)
	}
}

func TestRecordsRequests(t *testing.T) {
	s := fakeplatform.New(t)
	s.Stub("POST", "/v1/batches", fakeplatform.JSON(200, map[string]bool{"ack": true}))

	req, err := http.NewRequest(http.MethodPost, s.URL()+"/v1/batches?batch_id=b1", bytes.NewReader([]byte{0x28, 0xb5, 0x2f, 0xfd}))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer device-token-fake")
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
	if got.URL != "/v1/batches?batch_id=b1" {
		t.Errorf("URL = %q, want /v1/batches?batch_id=b1", got.URL)
	}
	if !bytes.Equal(got.Body, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Errorf("body = %x, want 28b52ffd", got.Body)
	}
	if got.Header.Get("Authorization") != "Bearer device-token-fake" {
		t.Errorf("Authorization = %q, want recorded verbatim", got.Header.Get("Authorization"))
	}
}
