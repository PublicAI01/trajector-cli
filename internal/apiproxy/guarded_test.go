package apiproxy_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// revokedTable grants one token and revokes the other, so the same
// request can be sent through a recorded and an unrecorded route.
func revokedTable(upstream string) string {
	return `{"projects":{
		"tok-live":{"project_id_hash":"h1","upstream":"` + upstream + `","granted_at":"2026-08-01T00:00:00Z"},
		"tok-revoked":{"project_id_hash":"h2","upstream":"` + upstream + `","granted_at":"2026-07-01T00:00:00Z","revoked_at":"2026-07-15T00:00:00Z"}
	}}`
}

func TestRecordingDoesNotChangeWhatIsSentUpstream(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(revokedTable(upstream.URL()))

	header := http.Header{
		"Content-Type":      {"application/json"},
		"Accept-Encoding":   {"br, gzip"},
		"Anthropic-Version": {"2023-06-01"},
		"Authorization":     {"Bearer sk-test-fake"},
	}
	body := `{"model":"claude-fable-5","messages":[{"role":"user","content":"hi"}]}`

	for _, token := range []string{"tok-live", "tok-revoked"} {
		upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_` + token + `"}`)})
		if resp := e.Post("/t/"+token+"/v1/messages", body, header); resp.StatusCode != 200 {
			t.Fatalf("%s status = %d", token, resp.StatusCode)
		}
	}

	reqs := upstream.Requests()
	if len(reqs) != 2 {
		t.Fatalf("upstream saw %d requests, want 2", len(reqs))
	}
	recorded, forwardedOnly := reqs[0], reqs[1]
	if string(recorded.Body) != string(forwardedOnly.Body) {
		t.Errorf("bodies diverged:\nrecorded: %s\nforwarded: %s", recorded.Body, forwardedOnly.Body)
	}
	for key, want := range forwardedOnly.Header {
		if got := recorded.Header[key]; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("header %s = %v when recorded, %v when only forwarded", key, got, want)
		}
	}
	for key := range recorded.Header {
		if _, ok := forwardedOnly.Header[key]; !ok {
			t.Errorf("header %s appears only when the exchange is recorded", key)
		}
	}
	if got := recorded.Header.Get("Accept-Encoding"); got == "br, gzip" {
		t.Error("the client's Accept-Encoding reached the upstream; the proxy normalizes it for every request")
	}
}

func TestOversizedExchangeIsDroppedWithoutTouchingForwarding(t *testing.T) {
	const limit = 64 << 10
	e := proxytest.New(t, proxytest.WithMaxRecordBytes(limit))
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	huge := `{"id":"msg_huge","filler":"` + strings.Repeat("x", 4<<20) + `"}`
	upstream.Enqueue(fakeupstream.Response{
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   []byte(huge),
	})

	resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(got) != len(huge) || string(got) != huge {
		t.Errorf("client received %d bytes, want all %d", len(got), len(huge))
	}

	h := e.WaitHealthz(func(h proxytest.Health) bool { return h.CapturesDropped > 0 })
	if len(h.RecentRecordingErrors) == 0 || !strings.Contains(h.RecentRecordingErrors[0], "capture limit") {
		t.Errorf("recorded errors = %v, want the capture limit", h.RecentRecordingErrors)
	}
	if h.RecordedToday != 0 {
		t.Errorf("recorded_today = %d, want the oversized exchange unrecorded", h.RecordedToday)
	}
	if stored := e.Rawcalls(); len(stored) != 0 {
		t.Errorf("spool holds %d rawcalls, want none", len(stored))
	}
}

func TestPanicInsideTheGuardedRegionDoesNotBreakForwarding(t *testing.T) {
	const limit = 64
	e := proxytest.New(t,
		proxytest.WithMaxRecordBytes(limit),
		proxytest.WithLogf(func(string, ...any) { panic("injected logging fault") }),
	)
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	body := `{"id":"msg_1","filler":"` + strings.Repeat("x", 4*limit) + `"}`
	upstream.Enqueue(fakeupstream.Response{
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   []byte(body),
	})

	resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || string(got) != body {
		t.Fatalf("status = %d, %d of %d bytes: the panic disturbed forwarding", resp.StatusCode, len(got), len(body))
	}

	// The proxy survived the panic and still accounts for the capture.
	h := e.WaitHealthz(func(h proxytest.Health) bool { return h.CapturesDropped > 0 })
	if len(h.RecentRecordingErrors) == 0 {
		t.Error("the dropped capture left no trace in healthz")
	}
	if stored := e.Rawcalls(); len(stored) != 0 {
		t.Errorf("spool holds %d rawcalls, want none", len(stored))
	}
}

func TestUnusableRouteUpstreamForwardsAtTheDefaultAndRecordsNothing(t *testing.T) {
	e := proxytest.New(t)
	defaultUp := e.Upstream
	e.WriteTable(activeTable("tok1", "not a url at all"))

	defaultUp.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_fallback"}`)})
	resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (%s), a broken table entry must not cost the user their traffic", resp.StatusCode, body)
	}
	if string(body) != `{"id":"msg_fallback"}` {
		t.Errorf("client body = %s", body)
	}
	if reqs := defaultUp.Requests(); len(reqs) != 1 || reqs[0].URL != "/v1/messages" {
		t.Errorf("default upstream requests = %+v", reqs)
	}

	h := e.WaitHealthz(func(h proxytest.Health) bool { return h.UnusableRouteUpstream > 0 })
	if h.RecordedToday != 0 {
		t.Errorf("recorded_today = %d, want nothing recorded for an unusable route", h.RecordedToday)
	}
	if stored := e.Rawcalls(); len(stored) != 0 {
		t.Errorf("spool holds %d rawcalls, want none", len(stored))
	}
}

func TestFinishedExchangeDoesNotWaitOnTheSpool(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(activeTable("tok1", upstream.URL()))

	const n = 20
	for i := 0; i < n; i++ {
		upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_q` + string(rune('a'+i)) + `"}`)})
	}
	for i := 0; i < n; i++ {
		if resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil); resp.StatusCode != 200 {
			t.Fatalf("request %d status = %d", i, resp.StatusCode)
		}
	}
	if stored := e.WaitRawcalls(n); len(stored) != n {
		t.Errorf("spool holds %d rawcalls, want %d", len(stored), n)
	}
}

// pausedTable grants a project and suspends recording device-wide, as
// signing out does.
func pausedTable(upstream, reason string) string {
	return `{
		"paused_reason":"` + reason + `",
		"projects":{"tok1":{
			"project_id_hash":"hash-tok1",
			"root_path":"/home/dev/project",
			"upstream":"` + upstream + `",
			"granted_at":"2026-08-01T00:00:00Z"
		}}
	}`
}

func TestPausedDeviceForwardsWithoutRecording(t *testing.T) {
	e := proxytest.New(t)
	upstream := e.Upstream
	e.WriteTable(pausedTable(upstream.URL(), "signed_out"))

	respBody := `{"id":"msg_paused"}`
	upstream.Enqueue(fakeupstream.Response{Body: []byte(respBody)})
	resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != respBody {
		t.Fatalf("pausing recording changed the exchange: %d %s", resp.StatusCode, body)
	}
	if reqs := upstream.Requests(); len(reqs) != 1 || reqs[0].URL != "/v1/messages" {
		t.Errorf("upstream requests = %+v, want the traffic still forwarded at the project upstream", reqs)
	}
	if stored := e.Rawcalls(); len(stored) != 0 {
		t.Errorf("spool holds %d rawcalls, want none while paused", len(stored))
	}
	if h := e.Healthz(); h.RecordedToday != 0 {
		t.Errorf("recorded_today = %d while paused", h.RecordedToday)
	}
}

func TestSelfcheckCarriesThePauseReason(t *testing.T) {
	e := proxytest.New(t)
	e.WriteTable(pausedTable(e.Upstream.URL(), "signed_out"))

	reply := e.Selfcheck("tok1")
	if !reply.TokenKnown || reply.Recording {
		t.Fatalf("selfcheck = %+v, want a known token that is not recording", reply)
	}
	if reply.Decision != "paused" || reply.PauseReason != "signed_out" {
		t.Errorf("decision/reason = %q/%q, want the pause to survive the seam", reply.Decision, reply.PauseReason)
	}
}
