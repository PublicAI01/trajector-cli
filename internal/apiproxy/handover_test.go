package apiproxy_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/capture"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/routing"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// longReplyBlocks is a well-formed streamed reply delivered in blocks
// spaced longReplyGap apart, so that it outlasts the harness's
// one-second drain budget by about a second.
var longReplyBlocks = []string{
	"event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_long","type":"message","role":"assistant","model":"claude-fable-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n",
	"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
	"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a long answer"}}` + "\n\n",
	"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n",
	"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}` + "\n\n",
	"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n",
}

const longReplyGap = 400 * time.Millisecond

// startLongReply sends a streamed request through the proxy at e and
// returns once the first block has arrived, so the exchange is in
// flight. The returned function reads the rest of the reply and
// returns everything that arrived.
func startLongReply(t *testing.T, e *proxytest.Env) func() string {
	t.Helper()
	return startReply(t, e, longReplyGap)
}

// startReply is startLongReply with the blocks spaced gap apart.
func startReply(t *testing.T, e *proxytest.Env, gap time.Duration) func() string {
	t.Helper()
	e.Upstream.Enqueue(fakeupstream.Response{SSE: longReplyBlocks, Delay: gap})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.TokenURL("tok1")+"/v1/messages", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	first := make([]byte, len(longReplyBlocks[0]))
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	return func() string {
		rest, _ := io.ReadAll(resp.Body)
		return string(first) + string(rest)
	}
}

// drainQuery is the query string of a drain that gives cause.
func drainQuery(cause string) string {
	return "?" + apiproxy.DrainCauseParam + "=" + cause
}

func TestDrainBudgetFollowsTheCauseTheDrainGives(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		completes bool
	}{
		{name: "a handover lets a reply longer than the drain budget finish", query: drainQuery(apiproxy.DrainCauseHandover), completes: true},
		{name: "a drain with no cause cuts the reply at the drain budget", query: "", completes: false},
		{name: "a cause the proxy does not know waits the drain budget", query: drainQuery("uninstall"), completes: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := proxytest.New(t)
			e.WriteTable(activeTable(e.Upstream.URL()))
			finish := startLongReply(t, e)

			if resp := e.PostAdmin(apiproxy.DrainPath + tc.query); resp.StatusCode != http.StatusAccepted {
				t.Fatalf("drain status = %d, want 202", resp.StatusCode)
			}
			got := finish()
			want := strings.Join(longReplyBlocks, "")
			if complete := got == want; complete != tc.completes {
				t.Errorf("reply complete = %v (%d of %d bytes), want %v", complete, len(got), len(want), tc.completes)
			}
			if err := e.WaitStopped(5 * time.Second); err != nil {
				t.Errorf("Serve = %v after the drain, want nil", err)
			}
		})
	}
}

func TestHandoverReleasesThePortAtOnceAndRecordsTheWholeReply(t *testing.T) {
	e := proxytest.New(t)
	e.WriteTable(activeTable(e.Upstream.URL()))
	finish := startLongReply(t, e)

	if resp := e.PostAdmin(apiproxy.DrainPath + drainQuery(apiproxy.DrainCauseHandover)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("drain status = %d, want 202", resp.StatusCode)
	}
	// The successor binds while the reply is still streaming here: the
	// longer wait is the old proxy's alone, never the takeover's.
	successor := bindWithin(t, e.Addr(), 500*time.Millisecond)
	successor.Close()

	if got, want := finish(), strings.Join(longReplyBlocks, ""); got != want {
		t.Fatalf("reply = %d of %d bytes, want the whole reply delivered on the old proxy", len(got), len(want))
	}
	if err := e.WaitStopped(5 * time.Second); err != nil {
		t.Fatalf("Serve = %v after the handover, want nil", err)
	}
	stored := e.Rawcalls()
	if len(stored) != 1 {
		t.Fatalf("spool holds %d rawcalls after the handover, want the one reply", len(stored))
	}
	if env := readEnvelope(t, stored[0]); env.Garbled() || stored[0].RequestID != "msg_long" {
		t.Errorf("rawcall %s garbled=%v, want the whole reply recorded as msg_long", stored[0].RequestID, env.Garbled())
	}
}

func TestADrainLeavesADataDirectoryDeletedDuringItDeleted(t *testing.T) {
	cases := []struct {
		name  string
		query string
		// gap spaces the reply's blocks: past the harness's one-second
		// drain budget on a handover, which waits for it, and well
		// inside it otherwise, so the reply ends either way.
		gap time.Duration
	}{
		{name: "a handover drops the capture and creates nothing", query: drainQuery(apiproxy.DrainCauseHandover), gap: longReplyGap},
		{name: "a drain with no cause drops the capture and creates nothing", query: "", gap: 20 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layout := proxytest.SeparateDirsLayout(t)
			e := proxytest.New(t, proxytest.WithLayout(layout))
			e.WriteTable(activeTable(e.Upstream.URL()))
			finish := startReply(t, e, tc.gap)

			if resp := e.PostAdmin(apiproxy.DrainPath + tc.query); resp.StatusCode != http.StatusAccepted {
				t.Fatalf("drain status = %d, want 202", resp.StatusCode)
			}
			// What uninstall deletes first. The routing table, in the
			// config directory, still grants recording, so the consent
			// check at write time lets the capture through.
			data := filepath.Dir(layout.SpoolDir())
			if err := os.RemoveAll(data); err != nil {
				t.Fatal(err)
			}

			if got, want := finish(), strings.Join(longReplyBlocks, ""); got != want {
				t.Fatalf("reply = %d of %d bytes, want the whole reply forwarded", len(got), len(want))
			}
			if err := e.WaitStopped(5 * time.Second); err != nil {
				t.Fatalf("Serve = %v after the drain, want nil", err)
			}
			if _, err := os.Stat(data); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("data directory after the drain: %v, want it still deleted", err)
			}
		})
	}
}

func TestADrainStillRecordsIntoADataDirectoryThatStands(t *testing.T) {
	e := proxytest.New(t, proxytest.WithLayout(proxytest.SeparateDirsLayout(t)))
	e.WriteTable(activeTable(e.Upstream.URL()))
	finish := startReply(t, e, 20*time.Millisecond)

	if resp := e.PostAdmin(apiproxy.DrainPath); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("drain status = %d, want 202", resp.StatusCode)
	}
	if got, want := finish(), strings.Join(longReplyBlocks, ""); got != want {
		t.Fatalf("reply = %d of %d bytes, want the whole reply forwarded", len(got), len(want))
	}
	if err := e.WaitStopped(5 * time.Second); err != nil {
		t.Fatalf("Serve = %v after the drain, want nil", err)
	}
	if stored := e.Rawcalls(); len(stored) != 1 {
		t.Errorf("spool holds %d rawcalls after the drain, want the reply recorded", len(stored))
	}
}

func TestAProxyExitingOnItsStopSignalLeavesADataDirectoryDeletedMeanwhileDeleted(t *testing.T) {
	layout := proxytest.SeparateDirsLayout(t)
	upstream := fakeupstream.New(t)
	if err := os.MkdirAll(filepath.Dir(layout.RoutingTable()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.RoutingTable(), []byte(activeTable(upstream.URL())), 0o600); err != nil {
		t.Fatal(err)
	}
	sp, err := spool.Create(layout.SpoolDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	dialect := capture.Anthropic
	dialect.OfficialUpstream = upstream.URL()
	server, err := apiproxy.New(apiproxy.Config{
		Version:         "1.2.3",
		Table:           routing.OpenStore(layout.RoutingTable()).Table(),
		Dialect:         dialect,
		DefaultUpstream: upstream.URL(),
		Spool:           sp,
		IdleTimeout:     time.Hour,
		DrainTimeout:    time.Second,
		AdminTokens:     layout,
	})
	if err != nil {
		t.Fatal(err)
	}
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.WithoutCancel(t.Context()))
	defer stop()
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, l) }()

	upstream.Enqueue(fakeupstream.Response{SSE: longReplyBlocks, Delay: 20 * time.Millisecond})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+l.Addr().String()+"/t/tok1/v1/messages", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := proxytest.Client(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, len(longReplyBlocks[0]))
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}

	// The stop signal a reboot or logout sends, then the deletion.
	stop()
	data := filepath.Dir(layout.SpoolDir())
	if err := os.RemoveAll(data); err != nil {
		t.Fatal(err)
	}

	rest, _ := io.ReadAll(resp.Body)
	if got, want := string(first)+string(rest), strings.Join(longReplyBlocks, ""); got != want {
		t.Fatalf("reply = %d of %d bytes, want the whole reply forwarded", len(got), len(want))
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve = %v after the stop signal, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not exit after the stop signal")
	}
	if _, err := os.Stat(data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("data directory after the exit: %v, want it still deleted", err)
	}
}

// bindWithin binds addr, retrying until within has passed.
func bindWithin(t *testing.T, addr string, within time.Duration) net.Listener {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		l, err := (&net.ListenConfig{}).Listen(context.WithoutCancel(t.Context()), "tcp", addr)
		if err == nil {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s could not be bound within %s of the drain: %v", addr, within, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
