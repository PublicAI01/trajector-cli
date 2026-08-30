package proxyserve_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/proxyserve"
)

// serve runs one serving process in the background and reports what it
// returned when it ends.
func (e *env) serve(stdout, stderr io.Writer) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- proxyserve.Serve(context.Background(), e.assembly, time.Hour, stdout, stderr)
	}()
	return done
}

func (e *env) waitExit(done <-chan error, within time.Duration) {
	e.t.Helper()
	select {
	case err := <-done:
		if err != nil {
			e.t.Fatalf("Serve = %v after a drain, want nil", err)
		}
	case <-time.After(within):
		e.t.Fatal("served proxy did not exit after a drain")
	}
}

func TestServeHostsCaptureAndTheFlushEndpoint(t *testing.T) {
	e := newEnv(t)
	e.sandbox.SeedRawcall("req-1", "hash-p1", time.Now().UTC())
	e.service.StubFunc("POST", "/v1/batches", ackBatch)

	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()

	reply := e.flush()
	if reply.Service != apiproxy.ServiceName || reply.Outcome != proxytest.Uploaded || reply.Records != 1 {
		t.Errorf("flush reply = %+v, want the seeded rawcall uploaded", reply)
	}

	if err := proxyserve.Serve(context.Background(), e.assembly, time.Hour, io.Discard, io.Discard); err != nil {
		t.Errorf("second Serve = %v, want quiet deferral to the healthy proxy", err)
	}

	e.adminPost(apiproxy.DrainPath)
	e.waitExit(served, 10*time.Second)
}

func TestServeRefusesAForeignPortHolder(t *testing.T) {
	e := newEnv(t)
	e.occupyPort()

	err := proxyserve.Serve(context.Background(), e.assembly, time.Hour, io.Discard, io.Discard)
	if !errors.Is(err, proxylife.ErrPortOccupied) {
		t.Errorf("Serve = %v, want ErrPortOccupied", err)
	}
}

func TestServeDefersToASiblingStillPublishingItsToken(t *testing.T) {
	e := newEnv(t)
	e.occupyPortStillPublishing()

	var stdout bytes.Buffer
	if err := proxyserve.Serve(context.Background(), e.assembly, time.Hour, &stdout, io.Discard); err != nil {
		t.Fatalf("Serve = %v, want a quiet deferral once the sibling proves itself", err)
	}
	if !strings.Contains(stdout.String(), "already running") {
		t.Errorf("stdout = %q, want the deferral to name the running proxy", stdout.String())
	}
}

func TestServeReportsAnUncontestedBindFailureAsItsOwnCause(t *testing.T) {
	e := newEnv(t)
	e.assembly.Addr = "127.0.0.1:99999"

	err := proxyserve.Serve(context.Background(), e.assembly, time.Hour, io.Discard, io.Discard)
	if err == nil || errors.Is(err, proxylife.ErrPortOccupied) {
		t.Fatalf("Serve = %v, want the listen failure itself, not an occupancy verdict", err)
	}
	if !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("Serve = %v, want the original listen error preserved", err)
	}
}

func TestServeReportsAPermissionRefusedBindAsItsOwnCause(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:1")
	if err == nil {
		probe.Close()
		t.Skip("this environment allows binding low ports without privilege")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Skipf("a low-port bind fails with %v here, not a permission refusal", err)
	}

	e := newEnv(t)
	e.assembly.Addr = "127.0.0.1:1"
	serveErr := proxyserve.Serve(context.Background(), e.assembly, time.Hour, io.Discard, io.Discard)
	if errors.Is(serveErr, proxylife.ErrPortOccupied) {
		t.Fatalf("Serve = %v, want the permission refusal, not an occupancy verdict", serveErr)
	}
	if !errors.Is(serveErr, os.ErrPermission) {
		t.Errorf("Serve = %v, want the permission refusal preserved", serveErr)
	}
}

func TestServeSurfacesASpoolFailure(t *testing.T) {
	e := newEnv(t)
	spoolDir := e.assembly.Layout.SpoolDir()
	if err := os.MkdirAll(filepath.Dir(spoolDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spoolDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := proxyserve.Serve(context.Background(), e.assembly, time.Hour, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "spool") {
		t.Errorf("Serve with a blocked spool = %v, want a spool error", err)
	}
}

func TestServeFinishesItsExitFlushBeforeReleasingThePort(t *testing.T) {
	e := newEnv(t)
	e.sandbox.SeedRawcall("req-1", "hash-p1", aged())

	portHeldDuringUpload := make(chan bool, 1)
	e.service.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		conn, err := net.DialTimeout("tcp", e.assembly.Addr, time.Second)
		if err == nil {
			conn.Close()
		}
		portHeldDuringUpload <- err == nil
		return ackBatch(r)
	})

	served := e.serve(io.Discard, io.Discard)
	e.waitHealthy()

	e.adminPost(apiproxy.DrainPath)
	e.waitExit(served, 10*time.Second)

	select {
	case held := <-portHeldDuringUpload:
		if !held {
			t.Error("the exit flush uploaded after the listen port was released")
		}
	default:
		t.Fatal("the drain exit never flushed the aged spool record")
	}
	if got := len(e.service.Requests()); got != 1 {
		t.Errorf("service saw %d requests, want the one exit-flush batch", got)
	}
	if n := len(e.sandbox.Rawcalls()); n != 0 {
		t.Errorf("spool holds %d rawcalls after the exit flush", n)
	}
}

func TestASlowExitFlushReleasesThePortAndLeavesItsRecordsToTheSuccessor(t *testing.T) {
	e := newEnv(t)
	e.assembly.Version = "1.0.0"
	e.sandbox.SeedRawcall("req-old", "hash-p1", aged())

	// The exit flush meets a link too slow to finish inside any window
	// the successor is willing to wait; every later upload is answered
	// at once.
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	slow := make(chan struct{}, 1)
	slow <- struct{}{}
	e.service.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		select {
		case <-slow:
			select {
			case <-released:
			case <-time.After(time.Minute):
			}
		default:
		}
		return ackBatch(r)
	})

	predecessor := e.serve(io.Discard, io.Discard)
	e.waitHealthy()

	takeover := proxylife.For(e.assembly.Layout, "2.0.0", "unspawnable", e.assembly.Addr).Ensure()
	if takeover == nil || !strings.Contains(takeover.Error(), "starting proxy") {
		t.Fatalf("takeover = %v, want it to have reached the point of starting the replacement", takeover)
	}
	if strings.Contains(takeover.Error(), "did not release the port") {
		t.Errorf("takeover = %v, want the exit flush to give up the port inside the successor's wait", takeover)
	}
	e.waitExit(predecessor, 20*time.Second)
	if n := len(e.sandbox.Rawcalls()); n != 1 {
		t.Fatalf("spool holds %d rawcalls after the abandoned exit flush, want the record kept for the successor", n)
	}

	successor := e.serve(io.Discard, io.Discard)
	e.waitHealthy()
	reply := e.flush()
	if reply.Outcome != proxytest.Uploaded || reply.Records != 1 {
		t.Errorf("successor flush = %+v, want the record the predecessor abandoned uploaded", reply)
	}

	e.adminPost(apiproxy.DrainPath)
	e.waitExit(successor, 20*time.Second)

	batchIDs := map[string]bool{}
	for _, r := range e.service.Requests() {
		b, err := parseBatch(r)
		if err != nil {
			t.Fatalf("unreadable upload request: %v", err)
		}
		if slices.Contains(b.RequestIDs, "req-old") {
			batchIDs[b.BatchID] = true
		}
	}
	if len(batchIDs) != 1 {
		t.Errorf("the abandoned record was offered under %d batch ids, want the pending id reused", len(batchIDs))
	}
	if n := len(e.sandbox.Rawcalls()); n != 0 {
		t.Errorf("spool holds %d rawcalls after the successor flushed, want 0", n)
	}
}

func TestProxyTakeoverNeverUploadsARecordUnderTwoBatchIDs(t *testing.T) {
	e := newEnv(t)
	addr := e.assembly.Addr
	e.sandbox.SeedRawcall("req-old", "hash-p1", aged())

	// The first upload is held open, standing in for a slow service, so
	// a successor wants the port while the predecessor is still
	// flushing. The first upload carrying req-new fails once, so the
	// batch id minted for it must survive the takeover to be retried.
	uploadStarted := make(chan struct{})
	holdFirst := make(chan struct{}, 1)
	holdFirst <- struct{}{}
	failNewOnce := make(chan struct{}, 1)
	failNewOnce <- struct{}{}
	e.service.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		b, err := parseBatch(r)
		if err != nil {
			return fakeplatform.JSON(590, map[string]any{"error": err.Error()})
		}
		select {
		case <-holdFirst:
			close(uploadStarted)
			time.Sleep(3 * time.Second)
		default:
		}
		if slices.Contains(b.RequestIDs, "req-new") {
			select {
			case <-failNewOnce:
				return fakeplatform.JSON(503, map[string]any{"error": "temporarily down"})
			default:
			}
		}
		return fakeplatform.JSON(200, map[string]any{"batch_id": b.BatchID})
	})

	predecessor := e.serve(io.Discard, io.Discard)
	e.waitHealthy()

	e.adminPost(apiproxy.DrainPath)
	select {
	case <-uploadStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the drain never started an exit flush")
	}

	// A record captured while the predecessor is still flushing: both
	// sides of the takeover now have records waiting to upload.
	e.sandbox.SeedRawcall("req-new", "hash-p1", aged())

	bound := make(chan struct{})
	successor := make(chan error, 1)
	go func() {
		// Take the port the moment it is free, as a newer binary's
		// takeover does after asking the old proxy to drain.
		for {
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err != nil {
				break
			}
			conn.Close()
			time.Sleep(5 * time.Millisecond)
		}
		close(bound)
		successor <- proxyserve.Serve(context.Background(), e.assembly, time.Hour, io.Discard, io.Discard)
	}()
	select {
	case <-bound:
	case <-time.After(20 * time.Second):
		t.Fatal("the port never came free for the successor")
	}
	e.waitHealthy()
	e.flush()

	e.waitExit(predecessor, 20*time.Second)

	if reply := e.flush(); reply.Outcome != proxytest.Empty {
		t.Errorf("flush after the takeover settled = %+v, want everything already uploaded", reply)
	}

	e.adminPost(apiproxy.DrainPath)
	e.waitExit(successor, 20*time.Second)

	batchesByRecord := map[string]map[string]bool{}
	for _, r := range e.service.Requests() {
		b, err := parseBatch(r)
		if err != nil {
			t.Fatalf("unreadable upload request: %v", err)
		}
		for _, rid := range b.RequestIDs {
			if batchesByRecord[rid] == nil {
				batchesByRecord[rid] = map[string]bool{}
			}
			batchesByRecord[rid][b.BatchID] = true
		}
	}
	for _, rid := range []string{"req-old", "req-new"} {
		if got := len(batchesByRecord[rid]); got != 1 {
			t.Errorf("%s was uploaded under %d batch ids, want exactly 1", rid, got)
		}
	}
	if n := len(e.sandbox.Rawcalls()); n != 0 {
		t.Errorf("spool holds %d rawcalls after the takeover, want 0", n)
	}
}

func TestSuperviseReturnsWhenTheContextIsAlreadyDone(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		proxyserve.Supervise(ctx, e.assembly, 0, io.Discard, io.Discard)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Supervise did not return on a dead context")
	}
}
