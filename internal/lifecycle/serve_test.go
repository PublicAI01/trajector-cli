package lifecycle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func waitHealthy(t *testing.T, e *env, addr string) {
	t.Helper()
	proxytest.WaitServing(t, e.client, addr, e.deps.Layout)
}

// adminPost posts to a served proxy's reserved endpoint with the admin
// token it published.
func adminPost(t *testing.T, e *env, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxytest.Authorize(req, e.deps.Layout)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestServeProxyHostsCaptureAndTheFlushEndpoint(t *testing.T) {
	e := newEnv(t)
	addr := freeAddr(t)
	e.deps.ProxyAddr = addr
	e.sandbox.SeedRawcall("req-1", "hash-p1", time.Now().UTC())
	e.service.StubFunc("POST", "/v1/batches", ackBatch(nil))

	served := make(chan error, 1)
	go func() {
		served <- e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	}()
	waitHealthy(t, e, addr)

	resp := adminPost(t, e, "http://"+addr+upload.FlushPath+"?force=1")
	defer resp.Body.Close()
	var reply upload.FlushReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Service != apiproxy.ServiceName || reply.Outcome != upload.Uploaded || reply.Records != 1 {
		t.Errorf("flush reply = %+v, want the seeded rawcall uploaded", reply)
	}

	if err := e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard); err != nil {
		t.Errorf("second ServeProxy = %v, want quiet deferral to the healthy proxy", err)
	}

	drain := adminPost(t, e, "http://"+addr+apiproxy.DrainPath)
	drain.Body.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("ServeProxy = %v after a drain, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("served proxy did not exit after a drain")
	}
}

func TestProjectSurfacesAnUnreadableTable(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(filepath.Dir(e.deps.Layout.RoutingTable()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.deps.Layout.RoutingTable(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := e.machine().Project(e.project); err == nil {
		t.Error("Project with an unreadable table = nil, want the error surfaced")
	}
}

func TestAnUnreadableTokenStoreReadsAsSignedOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not block reads on windows")
	}
	e := newEnv(t)
	if err := os.Chmod(e.deps.Layout.SecretsDir(), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(e.deps.Layout.SecretsDir(), 0o700) })

	if e.machine().Paired() {
		t.Error("an unreadable token store reported the device as paired")
	}
}

func TestServeProxyRefusesAForeignPortHolder(t *testing.T) {
	e := newEnv(t)
	e.occupyPort()

	err := e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	if !errors.Is(err, lifecycle.ErrPortOccupied) {
		t.Errorf("ServeProxy = %v, want ErrPortOccupied", err)
	}
}

func TestServeProxyDefersToASiblingStillPublishingItsToken(t *testing.T) {
	e := newEnv(t)
	e.occupyPortStillPublishing()

	var stdout bytes.Buffer
	if err := e.machine().ServeProxy(context.Background(), time.Hour, &stdout, io.Discard); err != nil {
		t.Fatalf("ServeProxy = %v, want a quiet deferral once the sibling proves itself", err)
	}
	if !strings.Contains(stdout.String(), "already running") {
		t.Errorf("stdout = %q, want the deferral to name the running proxy", stdout.String())
	}
}

func TestServeProxyReportsAnUncontestedBindFailureAsItsOwnCause(t *testing.T) {
	e := newEnv(t)
	e.deps.ProxyAddr = "127.0.0.1:99999"

	err := e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	if err == nil || errors.Is(err, lifecycle.ErrPortOccupied) {
		t.Fatalf("ServeProxy = %v, want the listen failure itself, not an occupancy verdict", err)
	}
	if !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("ServeProxy = %v, want the original listen error preserved", err)
	}
}

func TestServeProxyReportsAPermissionRefusedBindAsItsOwnCause(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:1")
	if err == nil {
		probe.Close()
		t.Skip("this environment allows binding low ports without privilege")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Skipf("a low-port bind fails with %v here, not a permission refusal", err)
	}

	e := newEnv(t)
	e.deps.ProxyAddr = "127.0.0.1:1"
	serveErr := e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	if errors.Is(serveErr, lifecycle.ErrPortOccupied) {
		t.Fatalf("ServeProxy = %v, want the permission refusal, not an occupancy verdict", serveErr)
	}
	if !errors.Is(serveErr, os.ErrPermission) {
		t.Errorf("ServeProxy = %v, want the permission refusal preserved", serveErr)
	}
}

func TestServeProxySurfacesASpoolFailure(t *testing.T) {
	e := newEnv(t)
	e.deps.ProxyAddr = freeAddr(t)
	if err := os.MkdirAll(filepath.Dir(e.deps.Layout.SpoolDir()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.deps.Layout.SpoolDir(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "spool") {
		t.Errorf("ServeProxy with a blocked spool = %v, want a spool error", err)
	}
}

func TestServeProxyFinishesItsExitFlushBeforeReleasingThePort(t *testing.T) {
	e := newEnv(t)
	addr := freeAddr(t)
	e.deps.ProxyAddr = addr
	e.sandbox.SeedRawcall("req-1", "hash-p1", time.Now().UTC().Add(-25*time.Hour))

	portHeldDuringUpload := make(chan bool, 1)
	e.service.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
		}
		portHeldDuringUpload <- err == nil
		return ackBatch(nil)(r)
	})

	served := make(chan error, 1)
	go func() {
		served <- e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	}()
	waitHealthy(t, e, addr)

	drain := adminPost(t, e, "http://"+addr+apiproxy.DrainPath)
	drain.Body.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("ServeProxy = %v after a drain, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("served proxy did not exit after a drain")
	}

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
	addr := freeAddr(t)
	e.deps.ProxyAddr = addr
	e.deps.Version = "1.0.0"
	e.sandbox.SeedRawcall("req-old", "hash-p1", time.Now().UTC().Add(-25*time.Hour))

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
		return ackBatch(nil)(r)
	})

	predecessor := make(chan error, 1)
	go func() {
		predecessor <- e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	}()
	waitHealthy(t, e, addr)

	takeover := proxylife.For(e.deps.Layout, "2.0.0", "unspawnable", addr).Ensure()
	if takeover == nil || !strings.Contains(takeover.Error(), "starting proxy") {
		t.Fatalf("takeover = %v, want it to have reached the point of starting the replacement", takeover)
	}
	if strings.Contains(takeover.Error(), "did not release the port") {
		t.Errorf("takeover = %v, want the exit flush to give up the port inside the successor's wait", takeover)
	}
	select {
	case err := <-predecessor:
		if err != nil {
			t.Fatalf("predecessor ServeProxy = %v after a drain, want nil", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the predecessor proxy did not exit")
	}
	if n := len(e.sandbox.Rawcalls()); n != 1 {
		t.Fatalf("spool holds %d rawcalls after the abandoned exit flush, want the record kept for the successor", n)
	}

	successor := make(chan error, 1)
	go func() {
		successor <- e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	}()
	waitHealthy(t, e, addr)
	flush := adminPost(t, e, "http://"+addr+upload.FlushPath+"?force=1")
	var reply upload.FlushReply
	if err := json.NewDecoder(flush.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	flush.Body.Close()
	if reply.Outcome != upload.Uploaded || reply.Records != 1 {
		t.Errorf("successor flush = %+v, want the record the predecessor abandoned uploaded", reply)
	}

	drain := adminPost(t, e, "http://"+addr+apiproxy.DrainPath)
	drain.Body.Close()
	select {
	case err := <-successor:
		if err != nil {
			t.Errorf("successor ServeProxy = %v, want nil", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the successor proxy did not exit")
	}

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
	addr := freeAddr(t)
	e.deps.ProxyAddr = addr
	e.sandbox.SeedRawcall("req-old", "hash-p1", time.Now().UTC().Add(-25*time.Hour))

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

	predecessor := make(chan error, 1)
	go func() {
		predecessor <- e.machine().ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	}()
	waitHealthy(t, e, addr)

	drain := adminPost(t, e, "http://"+addr+apiproxy.DrainPath)
	drain.Body.Close()
	select {
	case <-uploadStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the drain never started an exit flush")
	}

	// A record captured while the predecessor is still flushing: both
	// sides of the takeover now have records waiting to upload.
	e.sandbox.SeedRawcall("req-new", "hash-p1", time.Now().UTC().Add(-25*time.Hour))

	successorMachine := e.machine()
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
		successor <- successorMachine.ServeProxy(context.Background(), time.Hour, io.Discard, io.Discard)
	}()
	select {
	case <-bound:
	case <-time.After(20 * time.Second):
		t.Fatal("the port never came free for the successor")
	}
	waitHealthy(t, e, addr)
	firstFlush := adminPost(t, e, "http://"+addr+upload.FlushPath+"?force=1")
	firstFlush.Body.Close()

	select {
	case err := <-predecessor:
		if err != nil {
			t.Fatalf("predecessor ServeProxy = %v, want nil", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the predecessor proxy did not exit")
	}

	secondFlush := adminPost(t, e, "http://"+addr+upload.FlushPath+"?force=1")
	var reply upload.FlushReply
	if err := json.NewDecoder(secondFlush.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	secondFlush.Body.Close()
	if reply.Outcome != upload.Empty {
		t.Errorf("flush after the takeover settled = %+v, want everything already uploaded", reply)
	}

	drain = adminPost(t, e, "http://"+addr+apiproxy.DrainPath)
	drain.Body.Close()
	select {
	case err := <-successor:
		if err != nil {
			t.Errorf("successor ServeProxy = %v, want nil", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the successor proxy did not exit")
	}

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

func TestSuperviseProxyReturnsWhenTheContextIsAlreadyDone(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.machine().SuperviseProxy(ctx, 0, io.Discard, io.Discard)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("SuperviseProxy did not return on a dead context")
	}
}
