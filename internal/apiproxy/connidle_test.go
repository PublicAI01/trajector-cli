package apiproxy_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeupstream"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// TestConnIdleTimeoutClosesQuietKeptAliveConnection checks that a
// connection which finished a request and then sends nothing, or only
// part of the next request line, is closed by the proxy rather than
// held open.
func TestConnIdleTimeoutClosesQuietKeptAliveConnection(t *testing.T) {
	cases := []struct {
		name  string
		after string
	}{
		{name: "silent", after: ""},
		{name: "partial request line", after: "GE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := proxytest.New(t, proxytest.WithConnIdleTimeout(100*time.Millisecond))
			token := e.AdminToken()

			conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", e.Addr())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))

			req := "GET " + apiproxy.HealthzPath + " HTTP/1.1\r\n" +
				"Host: " + e.Addr() + "\r\n" +
				apiproxy.AdminHeader + ": " + token + "\r\n\r\n"
			if _, err := io.WriteString(conn, req); err != nil {
				t.Fatal(err)
			}
			br := bufio.NewReader(conn)
			resp, err := http.ReadResponse(br, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.Close {
				t.Fatalf("healthz = %d (close %v), want 200 on a kept-alive connection", resp.StatusCode, resp.Close)
			}

			if tc.after != "" {
				if _, err := io.WriteString(conn, tc.after); err != nil {
					t.Fatal(err)
				}
			}
			// The 5-second deadline is a backstop: the proxy is expected
			// to close the connection after about 100 ms.
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, err = br.ReadByte()
			if !errors.Is(err, io.EOF) {
				t.Fatalf("read after idle = %v, want io.EOF from the proxy closing the connection", err)
			}
		})
	}
}

// TestConnIdleTimeoutDoesNotCutForwardedExchange checks that the
// connection idle limit does not apply while a request is in flight: a
// request body that pauses and a streamed response whose blocks are
// spaced out, both for longer than the limit, still go through whole.
func TestConnIdleTimeoutDoesNotCutForwardedExchange(t *testing.T) {
	e := proxytest.New(t, proxytest.WithConnIdleTimeout(50*time.Millisecond))
	blocks := []string{
		"event: ping\ndata: {\"n\":1}\n\n",
		"event: ping\ndata: {\"n\":2}\n\n",
		"event: ping\ndata: {\"n\":3}\n\n",
	}
	e.Upstream.Enqueue(fakeupstream.Response{SSE: blocks, Delay: 200 * time.Millisecond})

	const first, second = `{"model":"claude-fable-5",`, `"stream":true}`
	pr, pw := io.Pipe()
	go func() {
		if _, err := io.WriteString(pw, first); err != nil {
			pw.CloseWithError(err)
			return
		}
		time.Sleep(200 * time.Millisecond)
		if _, err := io.WriteString(pw, second); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.BaseURL()+"/v1/messages", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the streamed response: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(got) != strings.Join(blocks, "") {
		t.Fatalf("response = %d %q, want 200 with every SSE block", resp.StatusCode, got)
	}

	reqs := e.Upstream.Requests()
	if len(reqs) != 1 || string(reqs[0].Body) != first+second {
		t.Fatalf("upstream received %d requests, want 1 with the whole body %q", len(reqs), first+second)
	}
}
