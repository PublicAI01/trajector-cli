package apiproxy_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestRecorder_KeepsUpstreamRequestID(t *testing.T) {
	cases := []struct {
		name         string
		header       string
		body         string
		wantID       string
		wantUpstream string
	}{
		{"response id and header", "req_hdr_1", `{"id":"msg_body_1","type":"message"}`, "msg_body_1", "req_hdr_1"},
		{"header only", "req_hdr_2", `{"type":"message"}`, "req_hdr_2", "req_hdr_2"},
		{"neither", "", `{"type":"message"}`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := proxytest.New(t)
			e.WriteTable(activeTable("tok1", e.Upstream.URL()))
			header := http.Header{"Content-Type": {"application/json"}}
			if tc.header != "" {
				header.Set("Request-Id", tc.header)
			}
			e.Upstream.Enqueue(fakeupstream.Response{Header: header, Body: []byte(tc.body)})

			resp := e.Post("/t/tok1/v1/messages", `{"model":"claude-fable-5","messages":[{"role":"user","content":"hi"}]}`, http.Header{"Content-Type": {"application/json"}})
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			stored := e.WaitRawcalls(1)
			env := readEnvelope(t, stored[0])
			if env.UpstreamRequestID() != tc.wantUpstream {
				t.Errorf("upstream_request_id = %q, want %q", env.UpstreamRequestID(), tc.wantUpstream)
			}
			switch {
			case tc.wantID != "" && env.RequestID() != tc.wantID:
				t.Errorf("request_id = %q, want %q", env.RequestID(), tc.wantID)
			case tc.wantID == "" && !strings.HasPrefix(env.RequestID(), "local-"):
				t.Errorf("request_id = %q, want a locally named record when no id was observed", env.RequestID())
			}
			if present := bytes.Contains(stored[0].Data, []byte(`"upstream_request_id"`)); present != (tc.wantUpstream != "") {
				t.Errorf("stored bytes carry upstream_request_id = %v, want %v (absent means absent, never a placeholder)", present, tc.wantUpstream != "")
			}
		})
	}
}
