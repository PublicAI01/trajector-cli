package apiproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// DefaultMaxRecordBytes bounds how much of one exchange the recorder
// holds in memory. Zero selects it. Past the limit the capture is
// abandoned and the exchange streams on untouched.
const DefaultMaxRecordBytes = 32 << 20

// recordQueueDepth bounds how many finished exchanges may wait to be
// written. A full queue drops the capture rather than making a request
// goroutine wait on disk.
const recordQueueDepth = 64

// finalizeTimeout bounds one queued capture. Work whose deadline has
// passed is dropped instead of run.
const finalizeTimeout = 30 * time.Second

// recorder is the proxy's guarded region. Everything that observes an
// exchange lives behind it, and nothing behind it may change what the
// client and upstream exchange or how long they take. Every entry point
// runs under guard, which absorbs panics; the byte limit bounds memory
// and the work queue bounds time, so an exchange can always be
// abandoned in favor of forwarding.
//
// A nil *recorder is the not-recording case and every method accepts it,
// so the forwarding path never branches on whether it is being watched.
type recorder struct {
	s        *Server
	route    routing.Route
	endpoint string
	hints    envelope.FormatHints
	limit    int64

	mu           sync.Mutex
	dropped      bool
	used         int64
	req          bytes.Buffer
	reqComplete  bool
	resp         bytes.Buffer
	respComplete bool
	status       int
	contentType  string
	encoding     string
	upstreamID   string
	finished     bool
}

func (s *Server) newRecorder(route routing.Route, endpoint string, hints envelope.FormatHints) *recorder {
	return &recorder{s: s, route: route, endpoint: endpoint, hints: hints, limit: s.cfg.MaxRecordBytes}
}

// observeRequest starts copying the request body as the transport reads
// it, so the recorder sees exactly the bytes sent upstream.
func (r *recorder) observeRequest(req *http.Request) {
	r.guard(func() {
		if req.Body == nil {
			r.mu.Lock()
			r.reqComplete = true
			r.mu.Unlock()
			return
		}
		req.Body = &tee{
			inner: req.Body,
			sink: func(p []byte, eof bool) {
				r.guard(func() { r.absorb(&r.req, &r.reqComplete, p, eof) })
			},
		}
	})
}

// observeResponse starts copying the upstream response and arranges for
// the capture to be finalized once the client is done reading it.
func (r *recorder) observeResponse(resp *http.Response) {
	r.guard(func() {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// Error responses are never stored: they have no value and
			// only widen the privacy surface.
			r.drop()
			return
		}
		r.mu.Lock()
		r.status = resp.StatusCode
		r.contentType = resp.Header.Get("Content-Type")
		r.encoding = resp.Header.Get("Content-Encoding")
		r.upstreamID = resp.Header.Get("request-id")
		r.mu.Unlock()

		resp.Body = &tee{
			inner: resp.Body,
			sink: func(p []byte, eof bool) {
				r.guard(func() { r.absorb(&r.resp, &r.respComplete, p, eof) })
			},
			onClose: r.finish,
		}
	})
}

// finish hands the finished exchange to the write queue and returns at
// once: the client's connection is never held open by disk I/O.
func (r *recorder) finish() {
	r.guard(func() {
		r.mu.Lock()
		skip := r.dropped || r.finished
		r.finished = true
		r.mu.Unlock()
		if skip {
			return
		}
		if !r.s.enqueue(r.write) {
			r.abandon("recorder: capture queue is full; rawcall not recorded")
		}
	})
}

// guard runs one step of the guarded region. A panic anywhere inside is
// counted and absorbed here: recording failure must never break
// forwarding, and this is the only place that has to be true.
func (r *recorder) guard(step func()) {
	if r == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			r.abandon("recorder panic: %v", p)
		}
	}()
	step()
}

// absorb copies observed bytes into a capture buffer, abandoning the
// capture rather than letting one exchange grow without bound.
func (r *recorder) absorb(buf *bytes.Buffer, complete *bool, p []byte, eof bool) {
	over := false
	func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		switch {
		case r.dropped:
		case r.used+int64(len(p)) > r.limit:
			over = true
		default:
			r.used += int64(len(p))
			buf.Write(p)
			if eof {
				*complete = true
			}
		}
	}()
	if over {
		r.abandon("recorder: exchange exceeds the %d byte capture limit; not recorded", r.limit)
	}
}

// drop abandons the capture silently, for outcomes that are expected
// rather than failures.
func (r *recorder) drop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropped = true
	r.req.Reset()
	r.resp.Reset()
}

// abandon gives up on this capture and says why, once.
func (r *recorder) abandon(format string, args ...any) {
	r.mu.Lock()
	already := r.dropped
	r.dropped = true
	r.req.Reset()
	r.resp.Reset()
	r.mu.Unlock()
	if already {
		return
	}
	r.s.stats.countDropped()
	r.s.stats.recordError(timestamped(format, args...))
	r.s.cfg.Logf(format, args...)
}

// write turns the observed exchange into a stored rawcall. It runs on
// the queue, never on a request goroutine.
func (r *recorder) write(ctx context.Context) {
	r.guard(func() {
		if err := ctx.Err(); err != nil {
			r.abandon("recorder: capture waited past its deadline; not recorded")
			return
		}
		obs, ok := r.observation()
		if !ok {
			return
		}
		env, err := envelope.Record(obs)
		if err != nil {
			r.abandon("%v", err)
			return
		}
		if err := r.s.cfg.Spool.Write(env); err != nil {
			r.abandon("spooling rawcall: %v", err)
			return
		}
		r.s.stats.recorded(obs.At, env.Garbled())
	})
}

// observation snapshots what was seen. The captured bytes are copied so
// nothing later can rewrite them out from under the writer.
func (r *recorder) observation() (envelope.Observation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dropped {
		return envelope.Observation{}, false
	}
	dialect := r.s.cfg.Dialect
	return envelope.Observation{
		Provider:      dialect.Provider,
		Endpoint:      r.endpoint,
		HTTPStatus:    r.status,
		ClientVersion: r.s.cfg.Version,
		ProjectIDHash: r.route.ProjectIDHash,
		At:            time.Now(),

		Upstream:         r.route.Upstream,
		OfficialUpstream: dialect.OfficialUpstream,

		Request:          bytes.Clone(r.req.Bytes()),
		RequestComplete:  r.reqComplete,
		Response:         bytes.Clone(r.resp.Bytes()),
		ResponseComplete: r.respComplete,

		ContentType:     r.contentType,
		ContentEncoding: r.encoding,

		Assembler: dialect.Assembler,

		UpstreamRequestID: r.upstreamID,
		Hints:             r.hints,
	}, true
}

// tee copies bytes to the recorder as they are read. Its own sink runs
// inside the guarded region, so Read and Close always return the inner
// result unchanged.
type tee struct {
	inner   io.ReadCloser
	sink    func(p []byte, eof bool)
	onClose func()
	closed  bool
}

func (t *tee) Read(p []byte) (int, error) {
	n, err := t.inner.Read(p)
	t.sink(p[:n], err == io.EOF)
	return n, err
}

func (t *tee) Close() error {
	err := t.inner.Close()
	if !t.closed {
		t.closed = true
		if t.onClose != nil {
			t.onClose()
		}
	}
	return err
}

// enqueue offers a finished capture to the write queue without ever
// blocking. A full or closed queue means the capture is lost, which is
// the correct trade: forwarding owes nothing to recording.
func (s *Server) enqueue(job func(context.Context)) bool {
	s.queueMu.RLock()
	defer s.queueMu.RUnlock()
	if s.queueClosed {
		return false
	}
	select {
	case s.records <- job:
		return true
	default:
		return false
	}
}

func (s *Server) runRecordQueue() {
	defer s.recordsDone.Done()
	for job := range s.records {
		ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
		job(ctx)
		cancel()
	}
}

// closeRecordQueue stops accepting captures and waits, up to limit, for
// those already queued to be written.
func (s *Server) closeRecordQueue(limit time.Duration) {
	s.queueMu.Lock()
	if !s.queueClosed {
		s.queueClosed = true
		close(s.records)
	}
	s.queueMu.Unlock()

	drained := make(chan struct{})
	go func() {
		s.recordsDone.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(limit):
	}
}

func timestamped(format string, args ...any) string {
	return time.Now().UTC().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...)
}
