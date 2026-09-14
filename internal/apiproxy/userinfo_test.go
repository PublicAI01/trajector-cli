package apiproxy_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// relayUpstream splices userinfo into a base URL, producing the shape a
// user configures when their relay authenticates that way.
func relayUpstream(url, userinfo string) string {
	return strings.Replace(url, "http://", "http://"+userinfo+"@", 1)
}

func basicAuth(userinfo string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(userinfo))
}

// TestUpstreamUserinfoReachesTheRelayAsBasicAuth pins what a recorded
// upstream's userinfo must become on the wire.
//
// `https://user:pass@relay` is a base URL Claude Code accepts and enable
// grants; the repository masks it everywhere it is written down
// precisely because it is a credential. But turning userinfo into an
// Authorization header is http.Client's work, and a ReverseProxy calls
// Transport.RoundTrip directly — so until 2026-09-14 the credential was
// simply dropped and the relay answered 401 to every request the moment
// trajector stood in the path. Nothing in the client's own traffic
// changed, which is what made it silent.
func TestUpstreamUserinfoReachesTheRelayAsBasicAuth(t *testing.T) {
	const userinfo = "relay-user:s3cr3t-relay-pw"
	e := proxytest.New(t)
	e.WriteTable(activeTable("tok1", relayUpstream(e.Upstream.URL(), userinfo)))

	post := func(t *testing.T, header http.Header) {
		t.Helper()
		e.Upstream.Enqueue(fakeupstream.Response{
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   []byte(`{"type":"message"}`),
		})
		header.Set("Content-Type", "application/json")
		resp := e.Post("/t/tok1/v1/messages", `{"model":"m"}`, header)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d, body %s", resp.StatusCode, body)
		}
	}

	post(t, http.Header{})
	// A credential of the client's own must never be displaced by the
	// upstream's, which is the rule http.Client applies.
	post(t, http.Header{"Authorization": {"Bearer sk-client-fake"}})

	reqs := e.Upstream.Requests()
	if len(reqs) != 2 {
		t.Fatalf("upstream saw %d requests, want 2", len(reqs))
	}
	if got, want := reqs[0].Header.Get("Authorization"), basicAuth(userinfo); got != want {
		t.Errorf("relay saw Authorization %q, want %q — the recorded upstream's credential was dropped", got, want)
	}
	if got := reqs[1].Header.Get("Authorization"); got != "Bearer sk-client-fake" {
		t.Errorf("relay saw Authorization %q, want the client's own header untouched", got)
	}
	for i, r := range reqs {
		if r.URL != "/v1/messages" {
			t.Errorf("request %d went to %s, want /v1/messages", i, r.URL)
		}
	}
}
