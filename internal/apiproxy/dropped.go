package apiproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// DroppedRecord is the durable account of the captures this device's
// proxies gave up on, by the reason each was given up: the same fixed
// categories healthz names, so a reader of either learns the other's
// words. It is cumulative and never reset by this build: a proxy
// restarts often (idle exit, handover, reboot), and a count that began
// at the last restart could not say since when a full spool has been
// refusing rawcalls. Its serialized layout is read by status, doctor,
// and the diagnostic bundle.
type DroppedRecord struct {
	Reasons map[string]DroppedReason `json:"reasons"`
}

// DroppedReason counts the captures given up for one reason. Bytes is
// what had been observed of each exchange when it was given up, which
// for a rawcall refused by the spool is the whole exchange.
type DroppedReason struct {
	Count   int       `json:"count"`
	Bytes   int64     `json:"bytes"`
	FirstAt time.Time `json:"first_at"`
	LastAt  time.Time `json:"last_at"`
}

// SpoolFull is the account of the rawcalls a full spool refused. A
// rawcall exists only in the moment it crosses the proxy, so each of
// these was not recorded, where a segment the spool refuses is only
// late.
func (r DroppedRecord) SpoolFull() DroppedReason { return r.Reasons[abandonSpoolFull] }

// Dropped keeps the DroppedRecord of one device current on disk. It
// counts in memory and leaves the disk to one writer of its own: a
// capture can be given up on a request goroutine, and while the spool
// is full every rawcall is given up, so a write per capture given up
// would put a disk flush on forwarding. The writer
// merges what this process counted into what the file holds, under the
// file's lock, so two proxies of one device — an exiting one still
// writing while its successor serves — each keep the other's count. A
// Dropped with no path counts in memory only.
type Dropped struct {
	path string
	// wake carries one pending signal to the writer: counts made while a
	// write runs are taken by the next one, however many there were.
	wake chan struct{}

	mu sync.Mutex
	// unwritten is what this process counted and the file does not hold
	// yet.
	unwritten DroppedRecord
}

// OpenDropped makes sure the directory of the record at path is there
// to write to, and reports a record already there that cannot be read.
// Such a record is overwritten by the next write: a record nothing can
// read holds no evidence worth keeping, and refusing to count would
// lose what comes next as well. The account returned counts whatever
// the error, in memory where it cannot on disk.
func OpenDropped(path string) (*Dropped, error) {
	d := &Dropped{path: path, wake: make(chan struct{}, 1)}
	if _, err := ReadDropped(path); err != nil {
		return d, err
	}
	return d, os.MkdirAll(filepath.Dir(path), 0o700)
}

// ReadDropped reads the record at path. A path with no file reads as
// an empty record and no error.
func ReadDropped(path string) (DroppedRecord, error) {
	var rec DroppedRecord
	data, err := fsatomic.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rec, nil
	}
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return DroppedRecord{}, fmt.Errorf("%s: %w", path, err)
	}
	return rec, nil
}

// count adds one capture given up for reason, observed to bytes, at
// now. It changes memory only and never waits for the writer.
func (d *Dropped) count(reason string, bytes int64, now time.Time) {
	d.mu.Lock()
	d.unwritten = d.unwritten.with(reason, DroppedReason{Count: 1, Bytes: bytes, FirstAt: now, LastAt: now})
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// start runs the writer until stop is called. stop writes what is
// still unwritten and returns once that write is done, so a caller that
// has stopped the writer finds every count made before the call on
// disk, or logged as not written. A capture given up after stop is
// counted in memory only.
func (d *Dropped) start(logf func(format string, args ...any)) (stop func()) {
	if d.path == "" {
		return func() {}
	}
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-d.wake:
				d.write(logf)
			case <-quit:
				d.write(logf)
				return
			}
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}

// write adds what is unwritten to the record on disk. What a failed
// write held is kept for the next one; a failure costs the proxy one
// log line and nothing else.
func (d *Dropped) write(logf func(format string, args ...any)) {
	d.mu.Lock()
	delta := d.unwritten
	d.unwritten = DroppedRecord{}
	d.mu.Unlock()
	if len(delta.Reasons) == 0 {
		return
	}
	err := fsatomic.Update(d.path, 0o600, func(old []byte) ([]byte, error) {
		var rec DroppedRecord
		if json.Unmarshal(old, &rec) != nil {
			rec = DroppedRecord{}
		}
		for reason, r := range delta.Reasons {
			rec = rec.with(reason, r)
		}
		return json.Marshal(rec)
	})
	if err == nil {
		return
	}
	logf("recorder: the record of dropped captures could not be written: %v", err)
	d.mu.Lock()
	for reason, r := range d.unwritten.Reasons {
		delta = delta.with(reason, r)
	}
	d.unwritten = delta
	d.mu.Unlock()
}

// with is r with one more account of reason folded in. It may change
// r's map in place.
func (r DroppedRecord) with(reason string, more DroppedReason) DroppedRecord {
	if r.Reasons == nil {
		r.Reasons = map[string]DroppedReason{}
	}
	r.Reasons[reason] = r.Reasons[reason].plus(more)
	return r
}

// plus is the account of the captures counted in a and in b.
func (a DroppedReason) plus(b DroppedReason) DroppedReason {
	a.Count += b.Count
	a.Bytes += b.Bytes
	if !b.FirstAt.IsZero() && (a.FirstAt.IsZero() || b.FirstAt.Before(a.FirstAt)) {
		a.FirstAt = b.FirstAt
	}
	if b.LastAt.After(a.LastAt) {
		a.LastAt = b.LastAt
	}
	return a
}
