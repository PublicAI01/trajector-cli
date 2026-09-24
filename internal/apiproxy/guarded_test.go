package apiproxy_test

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// revokedTable grants one token and revokes the other, so the same
// request can be sent through a recorded and an unrecorded route.
func revokedTable(upstream string) string {
	return `{"projects":{
		"tok-live":{"project_id_hash":"h1","upstream":"` + upstream + `","granted_at":"2026-08-01T00:00:00Z"},
		"tok-revoked":{"project_id_hash":"h2","upstream":"` + upstream + `","granted_at":"2026-07-01T00:00:00Z","revoked_at":"2026-07-15T00:00:00Z"}
	}}`
}

// withdrawnTable is what `trajector disable` leaves behind for one
// project — the grant revoked, kept so the route still forwards — while
// another project's grant stands.
func withdrawnTable(upstream string) string {
	return `{"projects":{
		"tok-live":{"project_id_hash":"h1","upstream":"` + upstream + `","granted_at":"2026-08-01T00:00:00Z","revoked_at":"2026-08-21T00:00:00Z"},
		"tok-other":{"project_id_hash":"h3","upstream":"` + upstream + `","granted_at":"2026-08-01T00:00:00Z"}
	}}`
}

// TestConsentWithdrawnMidExchangeStopsTheCapture pins where the
// recording decision has to hold. It is made when an exchange begins
// and acted on when the exchange ends, and for a streamed message those
// are minutes apart. `trajector disable` in between revokes the token
// and deletes the project's spooled records — but a capture that lands
// after that deletion is never looked at again, because the uploader
// does not consult consent, so it uploads data the user was told had
// been deleted. Until 2026-08-21 the verdict was only ever read at the
// start.
//
// The second exchange is the barrier: captures are written by one
// goroutine in the order they were queued, so once tok-other's record
// is in the spool, the in-flight capture has already had its turn.
func TestConsentWithdrawnMidExchangeStopsTheCapture(t *testing.T) {
	e := proxytest.New(t)
	e.WriteTable(revokedTable(e.Upstream.URL()))
	e.Upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_inflight"}`), Delay: 250 * time.Millisecond})

	done := make(chan struct{})
	go func() {
		defer close(done)
		req, err := http.NewRequest(http.MethodPost, e.BaseURL()+"/t/tok-live/v1/messages", strings.NewReader(`{"m":1}`))
		if err != nil {
			return
		}
		resp, err := e.Do(req)
		if err != nil {
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	time.Sleep(50 * time.Millisecond)
	e.WriteTable(withdrawnTable(e.Upstream.URL()))
	<-done

	e.Upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_after"}`)})
	if resp := e.Post("/t/tok-other/v1/messages", `{"m":1}`, nil); resp.StatusCode != 200 {
		t.Fatalf("barrier request status = %d", resp.StatusCode)
	}
	stored := e.WaitRawcalls(1)
	for _, rc := range stored {
		if rc.RequestID != "msg_after" {
			t.Errorf("spool holds %q; consent for that project was withdrawn while the exchange was still open, and disable has already deleted what it could see",
				rc.RequestID)
		}
	}
	if len(stored) != 1 {
		t.Errorf("spool holds %d rawcalls, want only the still-consenting project's", len(stored))
	}
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

// TestUnusableRouteUpstreamRefusesRatherThanRedirect pins where a grant
// that names nowhere has to stop. Until 2026-08-24 the proxy answered an
// unparseable route upstream by forwarding at the default upstream —
// which in production is the official endpoint — so a relay user's own
// credential headers went there instead, nothing was recorded, and the
// only trace was a healthz counter no surface prints. A grant exists to
// say this project's traffic goes somewhere else; when it cannot be
// read the destination is unknown, and unknown must not be answered
// with a guess that carries credentials.
//
// TestUnknownTokenForwardsToDefaultUpstreamUnrecorded holds the other
// half: a token naming no project has no recorded destination to
// contradict, so it still forwards at the default upstream.
func TestUnusableRouteUpstreamRefusesRatherThanRedirect(t *testing.T) {
	e := proxytest.New(t)
	defaultUp := e.Upstream
	e.WriteTable(activeTable("tok1", "not a url at all"))

	// Queued so that a redirect would succeed rather than fail for want
	// of a canned answer: this must fail on the redirect happening, not
	// on the fake upstream running dry.
	defaultUp.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_fallback"}`)})
	resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d (%s), want 502: a resolved token whose upstream cannot be parsed must not be forwarded anywhere", resp.StatusCode, body)
	}
	if reqs := defaultUp.Requests(); len(reqs) != 0 {
		t.Errorf("default upstream received %+v, want nothing: forwarding there carries this project's credential headers to a destination it never chose", reqs)
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
func pausedTable(upstream string, reason routing.PauseReason) string {
	return `{
		"paused_reason":"` + string(reason) + `",
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
	e.WriteTable(pausedTable(upstream.URL(), routing.PauseSignedOut))

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

// spoilers are the ways a routing table stops being readable. Moving
// the table aside is also what a user does to a table they were told
// cannot be read, and a proxy started with no table at all meets the
// same spoiler before anything was granted.
var spoilers = []struct {
	name  string
	spoil func(*testing.T, *proxytest.Sandbox)
}{
	{name: "a table that does not parse", spoil: func(_ *testing.T, s *proxytest.Sandbox) { s.CorruptRoutingTable() }},
	{name: "a table no read succeeds on", spoil: func(_ *testing.T, s *proxytest.Sandbox) { s.BlockRoutingTable() }},
	{name: "a table that is not there", spoil: moveTableAside},
}

func moveTableAside(t *testing.T, s *proxytest.Sandbox) {
	t.Helper()
	path := s.RoutingTablePath()
	if err := os.Rename(path, path+".aside"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestATableThatBreaksKeepsForwardingWhereItLastSaidAndRecordsNothing(t *testing.T) {
	for _, tt := range spoilers {
		t.Run(tt.name, func(t *testing.T) {
			e := proxytest.New(t)
			defaultUp := e.Upstream
			relay := fakeupstream.New(t)
			e.WriteTable(activeTable("tok1", relay.URL()))
			relay.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_read"}`)})
			e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
			e.WaitRawcalls(1)

			tt.spoil(t, e.Sandbox())
			time.Sleep(10 * time.Millisecond)

			respBody := `{"id":"msg_broken"}`
			relay.Enqueue(fakeupstream.Response{Body: []byte(respBody)})
			defaultUp.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_default"}`)})
			resp := e.Post("/t/tok1/v1/messages", `{"m":2}`, nil)
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 || string(body) != respBody {
				t.Fatalf("an unreadable table changed the exchange: %d %s", resp.StatusCode, body)
			}
			if reqs := relay.Requests(); len(reqs) != 2 {
				t.Errorf("relay requests = %+v, want both exchanges forwarded where the grant said", reqs)
			}
			if reqs := defaultUp.Requests(); len(reqs) != 0 {
				t.Errorf("default upstream received %+v, want nothing: the relay's credential headers must not reach it", reqs)
			}
			if stored := e.Rawcalls(); len(stored) != 1 {
				t.Errorf("spool holds %d rawcalls, want only the one recorded while the table could be read", len(stored))
			}

			e.Post("/t/never-granted/v1/messages", `{"m":3}`, nil)
			if reqs := defaultUp.Requests(); len(reqs) != 1 {
				t.Errorf("default upstream requests = %+v, want a token the last table did not name forwarded there, as before", reqs)
			}
		})
	}
}

func TestATableUnreadableSinceTheProxyStartedRefusesATokenRatherThanGuess(t *testing.T) {
	for _, tt := range spoilers {
		t.Run(tt.name, func(t *testing.T) {
			e := proxytest.New(t)
			tt.spoil(t, e.Sandbox())

			e.Upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_guess"}`)})
			resp := e.Post("/t/tok1/v1/messages", `{"m":1}`, nil)
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "trajector doctor") {
				t.Fatalf("status = %d (%s), want 502 naming `trajector doctor`", resp.StatusCode, body)
			}
			if strings.Contains(string(body), "routing") {
				t.Errorf("body = %s, want no internal detail in the reply", body)
			}
			if reqs := e.Upstream.Requests(); len(reqs) != 0 {
				t.Errorf("default upstream received %+v, want nothing forwarded to a guess", reqs)
			}
			if stored := e.Rawcalls(); len(stored) != 0 {
				t.Errorf("spool holds %d rawcalls, want none", len(stored))
			}
		})
	}
}

func TestSelfcheckOfATableNeverReadNamesNoUpstream(t *testing.T) {
	for _, tt := range spoilers {
		t.Run(tt.name, func(t *testing.T) {
			e := proxytest.New(t)
			tt.spoil(t, e.Sandbox())

			reply := e.Selfcheck("tok1")
			if reply.Decision != string(routing.NeverRead) || reply.TokenKnown || reply.Recording {
				t.Fatalf("selfcheck = %+v, want a token no table ever placed", reply)
			}
			if reply.UpstreamOrigin != "" {
				t.Errorf("upstream_origin = %q, want empty: nothing is forwarded for this token", reply.UpstreamOrigin)
			}
		})
	}
}

// TestPausedTrafficKeepsTheProxyAlive pins what the idle clock measures.
// A device-wide pause forwards every request and records none, and until
// 2026-08-16 only a recorded request touched the clock — so the clock
// never left the process start and the proxy drained itself one idle
// timeout after boot however much traffic was flowing through it. The
// next request of a live session then met a closed port. Traffic here
// keeps flowing for several timeouts; if the proxy exits under it, a
// request fails and this test says so.
func TestPausedTrafficKeepsTheProxyAlive(t *testing.T) {
	const idle = 300 * time.Millisecond
	e := proxytest.New(t, proxytest.WithIdleTimeout(idle))
	e.WriteTable(pausedTable(e.Upstream.URL(), routing.PauseSignedOut))

	started := time.Now()
	for i := 0; time.Since(started) < 3*idle; i++ {
		e.Upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_alive"}`)})
		req, err := http.NewRequest(http.MethodPost, e.BaseURL()+"/t/tok1/v1/messages", strings.NewReader(`{"m":1}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := e.Do(req)
		if err != nil {
			t.Fatalf("request %d failed %s into continuous forwarding: %v; the proxy stopped serving while it was still carrying traffic",
				i, time.Since(started).Round(time.Millisecond), err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("request %d status = %d, want the exchange forwarded untouched", i, resp.StatusCode)
		}
		time.Sleep(idle / 5)
	}
}

// TestUnresolvedTrafficKeepsTheProxyAlive is the other half of
// TestPausedTrafficKeepsTheProxyAlive. That one pinned that the idle
// clock must not be tied to the *recording* decision; this one pins that
// it must not be tied to the *routing* decision either.
//
// Until 2026-09-06 only a resolving token touched the clock, so a
// routing table that could not be read — unparseable JSON, a bad mode, a
// missing file under a standing injection — left it at the process start
// while every request still forwarded. The proxy then drained itself one
// idle timeout after boot however much traffic was flowing, and the next
// request of a live session met a closed port. Traffic here keeps
// flowing for several timeouts; if the proxy exits under it, a request
// fails and this test says so. The table here was read once and then
// broke, and it never named the token: the one of those states in which
// a token still forwards at the default upstream. A proxy that never
// read a table refuses a token instead of guessing where it goes.
func TestUnresolvedTrafficKeepsTheProxyAlive(t *testing.T) {
	const idle = 300 * time.Millisecond
	e := proxytest.New(t, proxytest.WithIdleTimeout(idle))
	e.WriteTable(`{"projects":{}}`)
	e.Upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_read"}`)})
	if resp := e.Post("/t/tok1/v1/messages", `{"m":0}`, nil); resp.StatusCode != 200 {
		t.Fatalf("status = %d while the table could be read, want the exchange forwarded", resp.StatusCode)
	}
	e.Sandbox().CorruptRoutingTable()

	started := time.Now()
	for i := 0; time.Since(started) < 3*idle; i++ {
		e.Upstream.Enqueue(fakeupstream.Response{Body: []byte(`{"id":"msg_alive"}`)})
		req, err := http.NewRequest(http.MethodPost, e.BaseURL()+"/t/tok1/v1/messages", strings.NewReader(`{"m":1}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := e.Do(req)
		if err != nil {
			t.Fatalf("request %d failed %s into continuous forwarding: %v; the proxy stopped serving while it was still carrying traffic an unreadable routing table could not name",
				i, time.Since(started).Round(time.Millisecond), err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("request %d status = %d, want the exchange forwarded untouched", i, resp.StatusCode)
		}
		time.Sleep(idle / 5)
	}
	if stored := e.Rawcalls(); len(stored) != 0 {
		t.Errorf("spool holds %d rawcalls, want none for a token no table resolves", len(stored))
	}
}

func TestSelfcheckCarriesThePauseReason(t *testing.T) {
	e := proxytest.New(t)
	e.WriteTable(pausedTable(e.Upstream.URL(), routing.PauseSignedOut))

	reply := e.Selfcheck("tok1")
	if !reply.TokenKnown || reply.Recording {
		t.Fatalf("selfcheck = %+v, want a known token that is not recording", reply)
	}
	if reply.Decision != "paused" || reply.PauseReason != string(routing.PauseSignedOut) {
		t.Errorf("decision/reason = %q/%q, want the pause to survive the seam", reply.Decision, reply.PauseReason)
	}
}
