package proxyserve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// calls records what a loop asked of its actions, in order.
type calls[T any] struct {
	mu  sync.Mutex
	got []T
}

func (c *calls[T]) add(v T) {
	c.mu.Lock()
	c.got = append(c.got, v)
	c.mu.Unlock()
}

func (c *calls[T]) list() []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got)
}

// flusher records the flushes a loop asks for and answers each with err.
type flusher struct {
	calls[bool]
	err error
}

func (f *flusher) flush(atOnce bool) error {
	f.add(atOnce)
	return f.err
}

// running starts loop and returns a channel closed when it returns.
func running(loop func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop()
	}()
	return done
}

func returned(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

type sweepResult struct{ read, cooled bool }

func TestPeriodicSweepSweepsOnTheWayInAndThenEveryInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		served := make(chan struct{})
		var sweeps calls[bool]
		sweep := func(onStart bool) (bool, bool) { sweeps.add(onStart); return false, false }
		done := running(func() {
			periodicSweep(t.Context(), served, sweep, new(flusher).flush, t.Logf)
		})

		synctest.Wait()
		if got := sweeps.list(); !slices.Equal(got, []bool{true}) {
			t.Fatalf("sweeps before the first interval = %v, want the one on the way in", got)
		}
		time.Sleep(3 * sweepInterval)
		synctest.Wait()
		if got := sweeps.list(); !slices.Equal(got, []bool{true, false, false, false}) {
			t.Errorf("sweeps after three intervals = %v, want the one on the way in and one per interval", got)
		}

		close(served)
		synctest.Wait()
		if !returned(done) {
			t.Error("the sweep cadence outlived the proxy it serves")
		}
	})
}

func TestPeriodicSweepEndsWithTheStopSignal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		var sweeps calls[bool]
		sweep := func(onStart bool) (bool, bool) { sweeps.add(onStart); return false, false }
		done := running(func() {
			periodicSweep(ctx, make(chan struct{}), sweep, new(flusher).flush, t.Logf)
		})

		synctest.Wait()
		stop()
		synctest.Wait()
		if !returned(done) {
			t.Fatal("the sweep cadence outlived the stop signal")
		}
		if got := sweeps.list(); len(got) != 1 {
			t.Errorf("sweeps = %v, want only the one on the way in", got)
		}
	})
}

func TestPeriodicSweepFlushesWhatEachSweepMadeDue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		swept []sweepResult
		want  []bool
	}{
		{"a sweep that read nothing flushes nothing", []sweepResult{{}, {}}, nil},
		{"what the sweep on the way in read gets a threshold check", []sweepResult{{read: true}, {}}, []bool{false}},
		{"what a later sweep read is sent at once", []sweepResult{{}, {read: true}}, []bool{true}},
		{"what the sweep on the way in cooled is sent at once", []sweepResult{{read: true, cooled: true}, {}}, []bool{true}},
		{"what a later sweep cooled is sent at once", []sweepResult{{}, {cooled: true}}, []bool{true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				served := make(chan struct{})
				n := 0
				sweep := func(bool) (bool, bool) {
					r := tc.swept[n]
					n++
					return r.read, r.cooled
				}
				var flushes flusher
				done := running(func() { periodicSweep(t.Context(), served, sweep, flushes.flush, t.Logf) })

				time.Sleep(sweepInterval)
				synctest.Wait()
				close(served)
				<-done
				if got := flushes.list(); !slices.Equal(got, tc.want) {
					t.Errorf("flushes = %v, want %v", got, tc.want)
				}
			})
		})
	}
}

func TestPeriodicSweepLogsAFailedFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		served := make(chan struct{})
		var logs calls[string]
		logf := func(format string, args ...any) { logs.add(fmt.Sprintf(format, args...)) }
		sweep := func(bool) (bool, bool) { return true, false }
		failing := flusher{err: errors.New("link down")}
		done := running(func() { periodicSweep(t.Context(), served, sweep, failing.flush, logf) })

		synctest.Wait()
		close(served)
		<-done
		if got := logs.list(); !slices.Equal(got, []string{"flush: link down"}) {
			t.Errorf("logs = %q, want the failed flush", got)
		}
	})
}

func TestPeriodicFlushChecksTheThresholdsEveryInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		served := make(chan struct{})
		var flushes flusher
		done := running(func() { periodicFlush(t.Context(), served, newFlushNudge(), flushes.flush, t.Logf) })

		synctest.Wait()
		if got := flushes.list(); len(got) != 0 {
			t.Fatalf("flushes before the first interval = %v, want none", got)
		}
		time.Sleep(3 * flushInterval)
		synctest.Wait()
		if got := flushes.list(); !slices.Equal(got, []bool{false, false, false}) {
			t.Errorf("flushes after three intervals = %v, want one threshold check per interval", got)
		}

		close(served)
		synctest.Wait()
		if !returned(done) {
			t.Error("the flush cadence outlived the proxy it serves")
		}
	})
}

func TestPeriodicFlushAnswersANudgeWithoutWaitingForTheInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		served := make(chan struct{})
		nudge := newFlushNudge()
		var flushes flusher
		done := running(func() { periodicFlush(t.Context(), served, nudge, flushes.flush, t.Logf) })

		nudge.ask(false)
		synctest.Wait()
		nudge.ask(true)
		synctest.Wait()
		if got := flushes.list(); !slices.Equal(got, []bool{false, true}) {
			t.Errorf("flushes = %v, want a threshold check, then every record at once", got)
		}

		close(served)
		<-done
	})
}

func TestPeriodicFlushAnswersTwoNudgesDuringOneFlushWithOneFlushForEveryRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		served := make(chan struct{})
		nudge := newFlushNudge()
		release := make(chan struct{})
		var flushes flusher
		flush := func(atOnce bool) error {
			err := flushes.flush(atOnce)
			if len(flushes.list()) == 1 {
				<-release
			}
			return err
		}
		done := running(func() { periodicFlush(t.Context(), served, nudge, flush, t.Logf) })

		nudge.ask(false)
		synctest.Wait()
		nudge.ask(true)
		nudge.ask(false)
		close(release)
		synctest.Wait()
		if got := flushes.list(); !slices.Equal(got, []bool{false, true}) {
			t.Errorf("flushes = %v, want the first nudge's, then one for every record", got)
		}

		close(served)
		<-done
	})
}

func TestPeriodicFlushDropsANudgeAfterTheStopSignal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		nudge := newFlushNudge()
		var flushes flusher

		stop()
		nudge.ask(true)
		periodicFlush(ctx, make(chan struct{}), nudge, flushes.flush, t.Logf)
		if got := flushes.list(); len(got) != 0 {
			t.Errorf("flushes = %v, want none after the stop signal", got)
		}
	})
}

func TestPeriodicFlushLogsAFailureAndTriesAgainNextInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		served := make(chan struct{})
		var logs calls[string]
		logf := func(format string, args ...any) { logs.add(fmt.Sprintf(format, args...)) }
		flushes := flusher{err: errors.New("link down")}
		done := running(func() { periodicFlush(t.Context(), served, newFlushNudge(), flushes.flush, logf) })

		time.Sleep(2 * flushInterval)
		synctest.Wait()
		close(served)
		<-done
		if got := len(flushes.list()); got != 2 {
			t.Errorf("flushes = %d, want one per interval despite the failure", got)
		}
		if got := logs.list(); !slices.Equal(got, []string{"flush: link down", "flush: link down"}) {
			t.Errorf("logs = %q, want each failure", got)
		}
	})
}

func TestPeriodicFlushEndsOnceTheExitFlushHasRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs calls[string]
		logf := func(format string, args ...any) { logs.add(fmt.Sprintf(format, args...)) }
		closed := flusher{err: fmt.Errorf("flushing: %w", upload.ErrClosed)}
		done := running(func() { periodicFlush(t.Context(), make(chan struct{}), newFlushNudge(), closed.flush, logf) })

		time.Sleep(flushInterval)
		synctest.Wait()
		if !returned(done) {
			t.Fatal("the flush cadence kept running after the exit flush")
		}
		if got := logs.list(); len(got) != 0 {
			t.Errorf("logs = %q, want none: the exit flush is no failure", got)
		}
	})
}
