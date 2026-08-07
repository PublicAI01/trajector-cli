package apiproxy_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

func readEnvelope(t *testing.T, r spool.Rawcall) envelope.Envelope {
	t.Helper()
	env, err := envelope.Parse(r.Data)
	if err != nil {
		t.Fatalf("rawcall %s: %v", r.RequestID, err)
	}
	return env
}

func activeTable(token, upstream string) string {
	rec, _ := json.Marshal(map[string]any{
		"projects": map[string]any{
			token: map[string]string{
				"project_id_hash": "hash-" + token,
				"root_path":       "/home/dev/project",
				"upstream":        upstream,
				"granted_at":      "2026-08-01T00:00:00Z",
			},
		},
	})
	return string(rec)
}

func TestAuthorizedCallForwardedVerbatimAndRecorded(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	respBody := `{"id":"msg_r1","type":"message","model":"claude-fable-5","usage":{"output_tokens":3}}`
	upstream.Enqueue(fakeupstream.Response{
		Header: http.Header{
			"Content-Type":      {"application/json"},
			"Request-Id":        {"req_abc123"},
			"X-Upstream-Marker": {"passed-through"},
		},
		Body: []byte(respBody),
	})

	reqBody := `{"model":"claude-fable-5","metadata":{"user_id":"session-key-1"},"messages":[{"role":"user","content":"hi"}]}`
	resp := e.Post("/t/tok1/v1/messages", reqBody, http.Header{
		"Authorization":     {"Bearer sk-test-fake"},
		"X-Api-Key":         {"sk-test-fake"},
		"Anthropic-Version": {"2023-06-01"},
		"Anthropic-Beta":    {"beta-a, beta-b"},
		"Content-Type":      {"application/json"},
	})
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	if string(body) != respBody {
		t.Errorf("client body = %s, want upstream bytes verbatim", body)
	}
	if resp.Header.Get("X-Upstream-Marker") != "passed-through" {
		t.Error("upstream response header not passed through")
	}

	reqs := upstream.Requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(reqs))
	}
	if reqs[0].Method != "POST" || reqs[0].URL != "/v1/messages" {
		t.Errorf("upstream saw %s %s, want POST /v1/messages", reqs[0].Method, reqs[0].URL)
	}
	if string(reqs[0].Body) != reqBody {
		t.Errorf("upstream body = %s, want client bytes verbatim", reqs[0].Body)
	}
	if reqs[0].Header.Get("Authorization") != "Bearer sk-test-fake" {
		t.Error("credential header not forwarded to upstream")
	}

	stored := e.WaitRawcalls(1)
	if stored[0].RequestID != "msg_r1" {
		t.Errorf("rawcall id %s, want msg_r1 from the response id", stored[0].RequestID)
	}
	env := readEnvelope(t, stored[0])
	if env.Endpoint() != "/v1/messages" || env.RequestID() != "msg_r1" {
		t.Errorf("endpoint/request_id = %s/%s", env.Endpoint(), env.RequestID())
	}
	if string(env.Request()) != reqBody {
		t.Errorf("recorded request = %s, want verbatim bytes", env.Request())
	}
	if string(env.Response()) != respBody {
		t.Errorf("recorded response = %s, want verbatim bytes", env.Response())
	}
	if env.HTTPStatus() != 200 || env.ProjectIDHash() != "hash-tok1" || env.UpstreamOrigin() != "official" || env.Garbled() {
		t.Errorf("capture = %d/%s/%s/%v", env.HTTPStatus(), env.ProjectIDHash(), env.UpstreamOrigin(), env.Garbled())
	}
	if env.AssembledBy() != "none" {
		t.Errorf("sse_assembly.by = %s for a non-streaming response", env.AssembledBy())
	}
	if env.Hints().AnthropicVersion != "2023-06-01" {
		t.Errorf("format hints version = %s", env.Hints().AnthropicVersion)
	}
	if len(env.Hints().AnthropicBeta) != 2 || env.Hints().AnthropicBeta[0] != "beta-a" {
		t.Errorf("format hints beta = %v", env.Hints().AnthropicBeta)
	}

	raw := stored[0].Data
	if bytes.Contains(raw, []byte("sk-test-fake")) {
		t.Error("credential value written to disk")
	}

	if stored[0].SessionKey != "session-key-1" {
		t.Errorf("indexed session key = %q, want the request's own", stored[0].SessionKey)
	}
}

func TestStreamedResponseReassembledIntoEnvelope(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_017","type":"message","role":"assistant","model":"claude-fable-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello world"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	// Split mid-line so reassembly cannot depend on chunk framing.
	upstream.Enqueue(fakeupstream.Response{SSE: []string{stream[:57], stream[57:200], stream[200:]}})

	resp := e.Post("/t/tok1/v1/messages", `{"model":"claude-fable-5","stream":true}`, nil)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != stream {
		t.Errorf("client stream diverged from upstream bytes:\n%s", body)
	}

	stored := e.WaitRawcalls(1)
	if stored[0].RequestID != "msg_017" {
		t.Errorf("rawcall id %s, want msg_017 from the assembled message id", stored[0].RequestID)
	}
	env := readEnvelope(t, stored[0])
	if env.Garbled() {
		t.Error("well-formed stream marked garbled")
	}
	const assembly = `"sse_assembly":{"by":"client","client_version":"1.2.3","rules_version":"1"}`
	if !strings.Contains(string(env.Bytes()), assembly) {
		t.Errorf("stored record does not carry %s:\n%s", assembly, env.Bytes())
	}
	var msg struct {
		ID      string `json:"id"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(env.Response(), &msg); err != nil {
		t.Fatalf("assembled response: %v", err)
	}
	if msg.ID != "msg_017" || len(msg.Content) != 1 || msg.Content[0].Text != "Hello world" || msg.Usage.OutputTokens != 12 {
		t.Errorf("assembled response = %s", env.Response())
	}
}

func TestGarbledStreamStoredRawWithMarker(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	stream := "event: mystery_event\ndata: {\"type\":\"mystery_event\"}\n\n"
	upstream.Enqueue(fakeupstream.Response{
		Header: http.Header{"Request-Id": {"req_garbled1"}},
		SSE:    []string{stream},
	})

	resp := e.Post("/t/tok1/v1/messages", `{"stream":true}`, nil)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != stream {
		t.Error("degraded recording altered the forwarded stream")
	}

	stored := e.WaitRawcalls(1)
	if stored[0].RequestID != "req_garbled1" {
		t.Errorf("rawcall id %s, want the upstream request id", stored[0].RequestID)
	}
	env := readEnvelope(t, stored[0])
	if !env.Garbled() {
		t.Error("degraded stream not marked garbled")
	}
	if env.AssembledBy() != "none" {
		t.Errorf("sse_assembly.by = %s, want none", env.AssembledBy())
	}
	var raw string
	if err := json.Unmarshal(env.Response(), &raw); err != nil {
		t.Fatalf("degraded response must be the raw stream as a string: %v", err)
	}
	if raw != stream {
		t.Errorf("stored stream = %q, want %q", raw, stream)
	}
}

func TestInterruptedStreamStoredPartialAndGarbled(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	first := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_cut","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"
	upstream.Enqueue(fakeupstream.Response{
		Header: http.Header{"Request-Id": {"req_cut1"}},
		SSE:    []string{first, "event: never_delivered\n", "data: {}\n\n"},
		Delay:  200 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", e.TokenURL("tok1")+"/v1/messages", strings.NewReader(`{"stream":true}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(first))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()

	stored := e.WaitRawcalls(1)
	if stored[0].RequestID != "req_cut1" {
		t.Errorf("rawcall id %s, want the upstream request id", stored[0].RequestID)
	}
	env := readEnvelope(t, stored[0])
	if !env.Garbled() {
		t.Error("interrupted response not marked garbled")
	}
	var raw string
	if err := json.Unmarshal(env.Response(), &raw); err != nil {
		t.Fatalf("partial response must be stored as a string: %v", err)
	}
	if !strings.Contains(raw, "msg_cut") {
		t.Errorf("received part missing from record: %q", raw)
	}
	if strings.Contains(raw, "never_delivered") {
		t.Errorf("record contains bytes the proxy never received: %q", raw)
	}
}

func TestUnknownTokenForwardsToDefaultUpstreamUnrecorded(t *testing.T) {
	e := proxytest.New(t)
	defaultUp := e.Upstream
	project := fakeupstream.New(t)
	e.WriteTable(activeTable("tok1", project.URL()))

	defaultUp.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_unknown"}`)})
	resp := e.Post("/t/revoked-or-bogus/v1/messages", `{"m":1}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("unknown token status = %d, forwarding must not break", resp.StatusCode)
	}
	if reqs := defaultUp.Requests(); len(reqs) != 1 || reqs[0].URL != "/v1/messages" {
		t.Fatalf("default upstream requests = %+v", reqs)
	}

	project.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_known"}`)})
	e.Post("/t/tok1/v1/messages", `{"m":2}`, nil)

	stored := e.WaitRawcalls(1)
	if len(stored) != 1 || stored[0].RequestID != "msg_known" {
		t.Errorf("rawcalls = %+v, only the authorized call may be recorded", stored)
	}
}

func TestRevokedTokenKeepsItsUpstreamButNeverRecords(t *testing.T) {
	e := proxytest.New(t)
	relay := fakeupstream.New(t)
	e.WriteTable(`{"projects":{
		"tok-revoked":{"project_id_hash":"h1","upstream":"` + relay.URL() + `","granted_at":"2026-07-01T00:00:00Z","revoked_at":"2026-07-15T00:00:00Z"},
		"tok-live":{"project_id_hash":"h2","upstream":"` + relay.URL() + `","granted_at":"2026-08-01T00:00:00Z"}
	}}`)

	relay.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_revoked"}`)})
	resp := e.Post("/t/tok-revoked/v1/messages", `{"m":1}`, nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != `{"id":"msg_revoked"}` {
		t.Fatalf("revoked token forwarding broken: %d %s", resp.StatusCode, body)
	}
	if reqs := relay.Requests(); len(reqs) != 1 {
		t.Fatalf("revoked traffic must still reach the project upstream, saw %d", len(reqs))
	}

	relay.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_live"}`)})
	e.Post("/t/tok-live/v1/messages", `{"m":2}`, nil)

	stored := e.WaitRawcalls(1)
	if len(stored) != 1 || stored[0].RequestID != "msg_live" {
		t.Errorf("rawcalls = %+v, revoked traffic must never be recorded", stored)
	}
}

func TestBarePathForwardsToDefaultUpstreamUnrecorded(t *testing.T) {
	e := proxytest.New(t)
	defaultUp := e.Upstream
	project := fakeupstream.New(t)
	e.WriteTable(activeTable("tok1", project.URL()))

	defaultUp.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_bare"}`)})
	resp := e.Post("/v1/messages", `{"m":1}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("bare path status = %d", resp.StatusCode)
	}
	if reqs := defaultUp.Requests(); len(reqs) != 1 || reqs[0].URL != "/v1/messages" {
		t.Fatalf("default upstream requests = %+v", reqs)
	}

	project.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_tagged"}`)})
	e.Post("/t/tok1/v1/messages", `{"m":2}`, nil)

	stored := e.WaitRawcalls(1)
	if len(stored) != 1 || stored[0].RequestID != "msg_tagged" {
		t.Errorf("rawcalls = %+v, bare-path traffic must never be recorded", stored)
	}
}

func TestSubPathAndNon2xxAreForwardedNotStored(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"input_tokens":12}`)})
	if resp := e.Post("/t/tok1/v1/messages/count_tokens", `{"m":1}`, nil); resp.StatusCode != 200 {
		t.Fatalf("count_tokens status = %d", resp.StatusCode)
	}

	upstream.Enqueue(fakeupstream.Response{Status: 429, Body: []byte(`{"type":"error"}`)})
	if resp := e.Post("/t/tok1/v1/messages", `{"m":2}`, nil); resp.StatusCode != 429 {
		t.Fatalf("limited status = %d, want 429 verbatim", resp.StatusCode)
	}

	upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_only"}`)})
	e.Post("/t/tok1/v1/messages", `{"m":3}`, nil)

	stored := e.WaitRawcalls(1)
	if len(stored) != 1 || stored[0].RequestID != "msg_only" {
		t.Errorf("rawcalls = %+v, want only the 2xx exact-endpoint call", stored)
	}
	if reqs := upstream.Requests(); len(reqs) != 3 || reqs[0].URL != "/v1/messages/count_tokens" {
		t.Errorf("upstream requests = %+v", reqs)
	}
}

func TestRecordingFailureDoesNotBreakForwarding(t *testing.T) {
	e := proxytest.New(t, proxytest.WithFullSpool())
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	respBody := `{"id":"msg_lost","usage":{"output_tokens":1}}`
	upstream.Enqueue(fakeupstream.Response{Body: []byte(respBody)})
	resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != respBody {
		t.Fatalf("forwarding broken by spool failure: %d %s", resp.StatusCode, body)
	}

	h := e.WaitHealthz(func(h proxytest.Health) bool { return len(h.RecentRecordingErrors) > 0 })
	if !strings.Contains(h.RecentRecordingErrors[0], "quota") {
		t.Errorf("recorded error = %q, want the quota failure", h.RecentRecordingErrors[0])
	}
}

func TestThirdPartyUpstreamChainedAndTagged(t *testing.T) {
	e := proxytest.New(t)
	relay := fakeupstream.New(t)
	e.WriteTable(activeTable("tok1", relay.URL()+"/relay/base"))

	relay.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_relay"}`)})
	resp := e.Post("/t/tok1/v1/messages?beta=true", `{"m":1}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	reqs := relay.Requests()
	if len(reqs) != 1 || reqs[0].URL != "/relay/base/v1/messages?beta=true" {
		t.Fatalf("relay requests = %+v, want the chained base path and query", reqs)
	}

	stored := e.WaitRawcalls(1)
	env := readEnvelope(t, stored[0])
	if env.UpstreamOrigin() != "third_party" {
		t.Errorf("upstream_origin = %s, want third_party", env.UpstreamOrigin())
	}
}

func TestOriginOracleIndependentOfDefaultUpstream(t *testing.T) {
	e := proxytest.New(t, proxytest.WithOfficialUpstream("https://api.example.com"))
	e.WriteTable(activeTable("tok1", e.Upstream.URL()))

	e.Upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_o1"}`)})
	resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	stored := e.WaitRawcalls(1)
	env := readEnvelope(t, stored[0])
	if env.UpstreamOrigin() != "third_party" {
		t.Errorf("upstream_origin = %s, want third_party when the official origin differs", env.UpstreamOrigin())
	}
	if sc := e.Selfcheck("tok1"); sc.UpstreamOrigin != "third_party" {
		t.Errorf("selfcheck upstream_origin = %s, want third_party", sc.UpstreamOrigin)
	}
}

func TestCompressedResponseRecordedDecoded(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	plain := `{"id":"msg_zip","usage":{"output_tokens":2}}`
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	zw.Write([]byte(plain))
	zw.Close()
	upstream.Enqueue(fakeupstream.Response{
		Header: http.Header{"Content-Encoding": {"gzip"}, "Content-Type": {"application/json"}},
		Body:   zipped.Bytes(),
	})

	resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != plain {
		t.Errorf("client body = %q, want transparently decoded bytes", body)
	}

	stored := e.WaitRawcalls(1)
	env := readEnvelope(t, stored[0])
	if env.Garbled() {
		t.Error("decodable compressed response marked garbled")
	}
	if string(env.Response()) != plain {
		t.Errorf("recorded response = %s, want decoded bytes", env.Response())
	}
}

func TestHotReloadedTableEnablesTokenWithoutRestart(t *testing.T) {
	e := proxytest.New(t)
	defaultUp := e.Upstream
	project := fakeupstream.New(t)

	defaultUp.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_before"}`)})
	if resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil); resp.StatusCode != 200 {
		t.Fatalf("pre-enable status = %d", resp.StatusCode)
	}

	e.WriteTable(activeTable("tok1", project.URL()))
	deadline := time.Now().Add(5 * time.Second)
	for len(e.Rawcalls()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("new table entry never took effect")
		}
		project.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_after"}`)})
		defaultUp.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_still_before"}`)})
		e.Post("/t/tok1/v1/messages", `{"m":2}`, nil)
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUpstreamFailureReturns502(t *testing.T) {
	e := proxytest.New(t)
	e.WriteTable(activeTable("tok1", "http://127.0.0.1:9"))

	resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestHealthzReportsIdentityAndCounters(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	health := e.Healthz()
	if health.Service != "trajector-proxy" || health.Version != "1.2.3" {
		t.Errorf("healthz = %+v", health)
	}
	if health.RecordedToday != 0 {
		t.Errorf("recorded_today = %d before any capture", health.RecordedToday)
	}

	upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_c1"}`)})
	e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
	e.WaitRawcalls(1)

	if health := e.Healthz(); health.RecordedToday != 1 {
		t.Errorf("recorded_today = %d after one capture", health.RecordedToday)
	}

	nf := e.PostAdmin("/trajector/unknown")
	if nf.StatusCode != 404 {
		t.Errorf("reserved namespace leaked: status %d", nf.StatusCode)
	}
}

func TestDrainRequestShutsDownGracefully(t *testing.T) {
	e := proxytest.New(t)

	resp := e.PostAdmin(apiproxy.DrainPath)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("drain status = %d, want 202", resp.StatusCode)
	}
	if err := e.WaitStopped(5 * time.Second); err != nil {
		t.Errorf("Serve returned %v after drain, want nil", err)
	}
	if _, err := net.DialTimeout("tcp", e.Addr(), 100*time.Millisecond); err == nil {
		t.Error("port still open after drain")
	}
}

func TestIdleProxyExitsOnItsOwn(t *testing.T) {
	e := proxytest.New(t, proxytest.WithIdleTimeout(50*time.Millisecond))
	if err := e.WaitStopped(5 * time.Second); err != nil {
		t.Errorf("Serve returned %v on idle exit, want nil", err)
	}
}

func TestReservedPathsAnswerNotFoundWithoutAMount(t *testing.T) {
	e := proxytest.New(t)
	resp := e.PostAdmin("/trajector/flush")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when nothing is mounted", resp.StatusCode)
	}
}

func TestMountedInternalHandlerServesUnderReservedPrefix(t *testing.T) {
	e := proxytest.New(t, proxytest.WithInternal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("mounted:" + r.URL.Path))
	})))

	resp := e.PostAdmin("/trajector/flush")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "mounted:/trajector/flush" {
		t.Errorf("mounted endpoint = %d %q", resp.StatusCode, body)
	}

	// The proxy's own built-ins stay its own: a mount must not be able
	// to impersonate healthz.
	h := e.Healthz()
	if h.Service != "trajector-proxy" {
		t.Errorf("healthz shadowed by the mount: %+v", h)
	}
}

func TestMountedCallDoesNotHoldTheProxyOpen(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e := proxytest.New(t,
		proxytest.WithIdleTimeout(100*time.Millisecond),
		proxytest.WithInternal(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			once.Do(func() { close(entered) })
			<-release
		})),
	)
	defer close(release)

	token := e.AdminToken()
	go func() {
		req, err := http.NewRequest(http.MethodPost, e.BaseURL()+"/trajector/flush", nil)
		if err != nil {
			return
		}
		req.Header.Set(apiproxy.AdminHeader, token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-entered

	if err := e.WaitStopped(5 * time.Second); err != nil {
		t.Errorf("Serve returned %v with a mounted call in flight, want a clean idle exit", err)
	}
}
