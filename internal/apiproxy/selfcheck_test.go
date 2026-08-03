package apiproxy_test

import (
	"net/http"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestSelfcheckConfirmsRecordingRouteWithoutUpstreamCall(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	reply := e.Selfcheck("tok1")
	want := proxytest.Selfcheck{
		Service:        apiproxy.ServiceName,
		Version:        "1.2.3",
		TokenKnown:     true,
		Recording:      true,
		Decision:       "record",
		ProjectIDHash:  "hash-tok1",
		UpstreamOrigin: "official",
		SpoolWritable:  true,
	}
	if reply != want {
		t.Errorf("selfcheck = %+v, want %+v", reply, want)
	}
	if n := len(upstream.Requests()); n != 0 {
		t.Errorf("selfcheck reached the upstream %d times", n)
	}
}

func TestSelfcheckUnknownTokenReportsNoRecording(t *testing.T) {
	e := proxytest.New(t)

	reply := e.Selfcheck("tok-unknown")
	if reply.TokenKnown || reply.Recording {
		t.Errorf("selfcheck for unknown token = %+v", reply)
	}
}

func TestSelfcheckRevokedTokenReportsNoRecording(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(`{"projects":{"tok1":{
		"project_id_hash": "hash-tok1",
		"root_path": "/home/dev/project",
		"upstream": "` + upstream.URL() + `",
		"granted_at": "2026-08-01T00:00:00Z",
		"revoked_at": "2026-08-02T00:00:00Z"
	}}}`)

	reply := e.Selfcheck("tok1")
	if !reply.TokenKnown || reply.Recording {
		t.Errorf("selfcheck for revoked token = %+v", reply)
	}
}

func TestSelfcheckMarksThirdPartyUpstream(t *testing.T) {
	e := proxytest.New(t)
	e.WriteTable(activeTable("tok1", "https://relay.example.com"))

	if reply := e.Selfcheck("tok1"); reply.UpstreamOrigin != "third_party" {
		t.Errorf("upstream_origin = %q, want third_party", reply.UpstreamOrigin)
	}
}

func TestTokenPrefixedInternalPathsAreNeverForwarded(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	resp, err := http.Get(e.TokenURL("tok1") + "/trajector/anything")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if n := len(upstream.Requests()); n != 0 {
		t.Errorf("reserved path reached the upstream %d times", n)
	}
}
