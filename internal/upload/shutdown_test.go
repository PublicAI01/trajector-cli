package upload_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

func TestCloseBoundsAnAlreadyRunningUpload(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	t.Cleanup(release)
	f.server.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		close(started)
		<-released
		return fakeplatform.Acknowledges(platform.Handshake{})(r)
	})
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.Acknowledges(platform.Handshake{}))
	f.storeRawcall(t, "req-shutdown", f.now)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	flushed := make(chan error, 1)
	go func() {
		_, err := f.uploader.Flush(ctx, true)
		flushed <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the upload did not reach the service")
	}

	closed := make(chan error, 1)
	go func() { closed <- f.uploader.Close(t.Context(), 100*time.Millisecond) }()
	budgetMissed := false
	select {
	case err := <-closed:
		if err == nil {
			t.Error("close succeeded despite an unacknowledged upload exceeding its budget")
		}
	case <-time.After(2 * time.Second):
		budgetMissed = true
		cancel()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("close did not finish after cancellation")
		}
	}
	select {
	case err := <-flushed:
		if err == nil {
			t.Error("the running upload succeeded without an acknowledgement")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close returned while the running flush was still active")
	}
	if budgetMissed {
		t.Fatal("close did not cancel the running upload within its shutdown budget")
	}
	if f.spool.Usage() == 0 {
		t.Error("shutdown deleted the unacknowledged record")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "pending.json")); err != nil {
		t.Fatalf("shutdown lost the pending batch: %v", err)
	}
	if _, err := f.uploader.Flush(t.Context(), true); !errors.Is(err, upload.ErrClosed) {
		t.Errorf("flush after close = %v, want ErrClosed", err)
	}
	release()
	if _, err := f.newUploader(t).Flush(t.Context(), true); err != nil {
		t.Fatalf("a successor could not resume the batch: %v", err)
	}
	requests := f.server.Requests()
	if len(requests) != 2 {
		t.Fatalf("service saw %d requests, want the abandoned attempt and its retry", len(requests))
	}
	if uploadedBatchID(t, requests[0]) != uploadedBatchID(t, requests[1]) {
		t.Error("the successor changed the pending batch identity")
	}
	if f.spool.Usage() != 0 {
		t.Error("the acknowledged retry did not remove its record")
	}
}

func TestCancellingCloseStopsTheRunningUploadWithoutTimeoutBackoff(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	f.server.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		close(started)
		<-released
		return fakeplatform.Acknowledges(platform.Handshake{})(r)
	})
	f.storeRawcall(t, "req-cancel", f.now)
	flushContext, cancelFlush := context.WithCancel(t.Context())
	defer cancelFlush()
	flushed := make(chan error, 1)
	go func() {
		_, err := f.uploader.Flush(flushContext, true)
		flushed <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the upload did not reach the service")
	}

	closeContext, cancelClose := context.WithCancel(t.Context())
	cancelClose()
	closed := make(chan error, 1)
	go func() { closed <- f.uploader.Close(closeContext, time.Minute) }()
	select {
	case err := <-closed:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("close = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled close did not stop the running upload")
	}
	select {
	case err := <-flushed:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("running flush = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close returned while the running flush was still active")
	}
	if f.standing(upload.TimedOut).Held() {
		t.Error("shutdown cancellation imposed a network-timeout backoff")
	}
	if f.spool.Usage() == 0 || f.uploadCount() != 1 {
		t.Error("cancelled shutdown removed the pending record or started another upload")
	}
}

func TestConcurrentClosersWaitForTheSameFinalFlush(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	t.Cleanup(release)
	f.server.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		close(started)
		<-released
		return fakeplatform.Acknowledges(platform.Handshake{})(r)
	})
	f.storeRawcall(t, "req-final", f.now)
	first := make(chan error, 1)
	go func() { first <- f.uploader.Close(t.Context(), 10*time.Second) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the final flush did not reach the service")
	}

	const callers = 8
	closed := make(chan error, callers)
	for range callers {
		go func() { closed <- f.uploader.Close(t.Context(), 0) }()
	}
	refused := make(chan error, 2)
	go func() {
		_, err := f.uploader.Flush(t.Context(), true)
		refused <- err
	}()
	go func() {
		_, err := f.uploader.FlushRecords(t.Context())
		refused <- err
	}()
	for range 2 {
		select {
		case err := <-refused:
			if !errors.Is(err, upload.ErrClosed) {
				t.Errorf("flush during close = %v, want ErrClosed", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a later flush waited for network activity instead of refusing shutdown")
		}
	}
	select {
	case err := <-closed:
		t.Fatalf("a concurrent close returned before the final upload settled: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case err := <-first:
		if err != nil {
			t.Errorf("first close = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first close did not settle its acknowledgement")
	}
	for range callers {
		select {
		case err := <-closed:
			if err != nil {
				t.Errorf("concurrent close = %v, want nil", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a concurrent close did not finish")
		}
	}
	if f.uploadCount() != 1 || f.spool.Usage() != 0 {
		t.Error("concurrent closes duplicated the final upload or failed to settle its acknowledgement")
	}
}

func TestCloseSettlesAnActiveAcknowledgementWithinItsBudget(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	t.Cleanup(release)
	f.server.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		close(started)
		<-released
		return fakeplatform.Acknowledges(platform.Handshake{})(r)
	})
	f.storeRawcall(t, "req-ack", f.now)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	flushed := make(chan error, 1)
	go func() {
		_, err := f.uploader.Flush(ctx, true)
		flushed <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the upload did not reach the service")
	}
	closed := make(chan error, 1)
	go func() { closed <- f.uploader.Close(t.Context(), 10*time.Second) }()
	select {
	case err := <-closed:
		t.Fatalf("close returned before the active upload settled: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	for _, result := range []<-chan error{flushed, closed} {
		select {
		case err := <-result:
			if err != nil {
				t.Errorf("upload or close = %v, want the in-budget acknowledgement settled", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("upload or close did not finish after acknowledgement")
		}
	}
	if f.uploadCount() != 1 || f.spool.Usage() != 0 {
		t.Error("the in-budget acknowledgement was not settled exactly once")
	}
}

func TestCloseWithNoBudgetKeepsRecordsWithoutStartingAnUpload(t *testing.T) {
	for _, budget := range []time.Duration{0, -time.Second} {
		t.Run(budget.String(), func(t *testing.T) {
			f := newFixture(t)
			f.storeRawcall(t, "req-no-budget", f.now)
			if err := f.uploader.Close(t.Context(), budget); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("close = %v, want context.DeadlineExceeded", err)
			}
			if f.spool.Usage() == 0 || f.uploadCount() != 0 {
				t.Error("an exhausted shutdown budget removed a record or contacted the service")
			}
			if _, err := f.uploader.Flush(t.Context(), true); !errors.Is(err, upload.ErrClosed) {
				t.Errorf("flush after close = %v, want ErrClosed", err)
			}
		})
	}
}

func TestQueuedFlushCannotStartAfterClose(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	f.server.StubFunc("POST", "/v1/batches", func(r fakeplatform.Request) fakeplatform.Response {
		close(started)
		<-released
		return fakeplatform.Acknowledges(platform.Handshake{})(r)
	})
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.Acknowledges(platform.Handshake{}))
	f.storeRawcall(t, "req-queued", f.now)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	active := make(chan error, 1)
	go func() {
		_, err := f.uploader.Flush(ctx, true)
		active <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the active upload did not reach the service")
	}
	queued := make(chan error, 1)
	go func() {
		_, err := f.uploader.FlushRecords(ctx)
		queued <- err
	}()
	closeContext, cancelClose := context.WithCancel(t.Context())
	cancelClose()
	closed := make(chan error, 1)
	go func() { closed <- f.uploader.Close(closeContext, time.Minute) }()
	for _, check := range []struct {
		result <-chan error
		want   error
	}{
		{active, context.Canceled},
		{queued, upload.ErrClosed},
		{closed, context.Canceled},
	} {
		select {
		case err := <-check.result:
			if !errors.Is(err, check.want) {
				t.Errorf("concurrent operation = %v, want %v", err, check.want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a concurrent operation did not stop during shutdown")
		}
	}
	if f.uploadCount() != 1 || f.spool.Usage() == 0 {
		t.Error("the queued flush uploaded after shutdown began")
	}
}
