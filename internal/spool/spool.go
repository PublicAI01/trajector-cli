// Package spool stores captured rawcalls on disk until upload. The
// layout is a documented product contract:
//
//	<dir>/<YYYYMMDD>/<request_id>.json   one serialized envelope
//	<dir>/<YYYYMMDD>/index.jsonl         advisory sidecar index
//
// Rawcall files are the source of truth; the index only accelerates
// batching and can always be rebuilt by rescanning the day directory.
// Directories are 0700 and files 0600: rawcalls hold unredacted data
// and must stay private to the user until masked and uploaded.
package spool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// DefaultQuota bounds total spool size. When the quota is reached the
// spool stops accepting writes but never evicts: captured rawcalls may
// correspond to compensation and must not be dropped silently.
const DefaultQuota = 2 << 30

// ErrQuotaExceeded reports a write refused because it would push the
// spool past its quota. Callers must treat this as stop-recording, not
// as a reason to delete anything.
var ErrQuotaExceeded = errors.New("spool: quota exceeded")

// indexName is the per-day sidecar index file name.
const indexName = "index.jsonl"

// indexLine is one record in the sidecar index. SessionKey groups
// records of the same coding session so upload batching can lay them
// out adjacently; ProjectIDHash lets consent withdrawal find a
// project's records without reading them.
type indexLine struct {
	RequestID     string `json:"request_id"`
	SessionKey    string `json:"session_key,omitempty"`
	Timestamp     string `json:"timestamp"`
	ProjectIDHash string `json:"project_id_hash,omitempty"`
}

// Spool is a bounded rawcall store, safe for concurrent writers. The
// bound belongs to the directory, not to this handle: usage converges
// on what is actually on disk, so records deleted through another
// handle — even in another process — free quota here too.
type Spool struct {
	dir   string
	quota int64

	mu    sync.Mutex
	usage int64
	// sig fingerprints the directory state usage was derived from. Any
	// other handle's mutation creates, removes, or renames a file, which
	// updates its day directory's name set or mtime; a mismatch means
	// usage must be re-derived before the next quota decision.
	sig string
}

// Create prepares the spool rooted at dir, creating the directory if
// needed, and computes current usage from disk. A quota of zero selects
// DefaultQuota.
func Create(dir string, quota int64) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return Open(dir, quota)
}

// Open reads the spool rooted at dir without creating anything. A
// directory that was never written to opens as an empty spool: readers
// see nothing and a later write still creates what it needs.
func Open(dir string, quota int64) (*Spool, error) {
	if quota == 0 {
		quota = DefaultQuota
	}
	sig := dirSignature(dir)
	usage, err := walkUsage(dir)
	if err != nil {
		return nil, err
	}
	return &Spool{dir: dir, quota: quota, usage: usage, sig: sig}, nil
}

// walkUsage derives total spool size from disk, the authority the
// in-memory figure must always converge on.
func walkUsage(dir string) (int64, error) {
	var usage int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		usage += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	return usage, nil
}

// dirSignature fingerprints the day directories by name and mtime. It
// is deliberately cheap — one directory listing plus one stat per day —
// so quota decisions can verify it without walking every record.
func dirSignature(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var b []byte
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		b = fmt.Appendf(b, "%s/%d;", e.Name(), info.ModTime().UnixNano())
	}
	return string(b)
}

// refreshLocked re-derives usage when the signature says another handle
// has changed the directory. The signature is a cheap approximation and
// can miss a change, so it is only ever trusted to say "nothing new" on
// the path where being wrong is harmless — see rederiveLocked for the
// path where it isn't.
func (s *Spool) refreshLocked() {
	if dirSignature(s.dir) == s.sig {
		return
	}
	s.rederiveLocked()
}

// rederiveLocked walks the spool and takes the result as the truth,
// whatever the signature claims.
//
// Directory mtime has the granularity of the kernel's coarse clock, a
// few milliseconds. A foreign delete landing in the same tick as this
// handle's last observation leaves the signature identical, so
// refreshLocked skips the walk and the handle keeps counting bytes that
// are already gone. On the accept path that costs nothing — usage
// converges on the next mutation. On the refuse path it is a rawcall
// dropped for a quota that is not actually full, which is exactly the
// data loss the quota exists to make orderly.
//
// So callers pay for a walk before refusing, and only before refusing:
// refusal is rare, and being wrong about it is not recoverable.
func (s *Spool) rederiveLocked() {
	// Snapshot the signature before the walk, not after: a change that
	// lands mid-walk then leaves the two disagreeing, and the next
	// refresh re-derives. Snapshotting after would fold that change into
	// the signature and strand the stale figure.
	sig := dirSignature(s.dir)
	usage, err := walkUsage(s.dir)
	if err != nil {
		return
	}
	s.usage = usage
	s.sig = sig
}

// wouldExceedLocked reports whether extra more bytes would put usage
// over quota, re-deriving from disk before answering yes: the bytes
// being counted may have been freed by another handle without the
// signature noticing, and a wrong yes drops a rawcall.
func (s *Spool) wouldExceedLocked(extra int64) bool {
	if s.usage+extra <= s.quota {
		return false
	}
	s.rederiveLocked()
	return s.usage+extra > s.quota
}

// Write stores one rawcall atomically and appends it to the day's
// index. Every indexed fact — id, session key, timestamp, project — is
// derived here from the envelope itself, so the index can never
// disagree with the record it describes.
func (s *Spool) Write(env envelope.Envelope) error {
	id := env.RequestID()
	// The spool builds a file path from the id and must not trust it.
	if !envelope.ValidRequestID(id) {
		return fmt.Errorf("spool: invalid request id %q", id)
	}
	at := env.Timestamp()
	if at.IsZero() {
		return fmt.Errorf("spool: rawcall %s carries no capture timestamp", id)
	}
	data := env.Bytes()
	line, err := json.Marshal(indexLine{
		RequestID:     id,
		SessionKey:    env.SessionKey(),
		Timestamp:     at.UTC().Format(time.RFC3339Nano),
		ProjectIDHash: env.ProjectIDHash(),
	})
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()

	day := at.UTC().Format("20060102")
	dayDir := filepath.Join(s.dir, day)
	final := filepath.Join(dayDir, id+".json")
	var replaced int64
	if info, err := os.Stat(final); err == nil {
		replaced = info.Size()
	}
	needed := int64(len(data)) + int64(len(line))
	if s.wouldExceedLocked(needed - replaced) {
		return ErrQuotaExceeded
	}

	if err := os.MkdirAll(dayDir, 0o700); err != nil {
		return err
	}
	if err := fsatomic.WriteFile(final, data, 0o600); err != nil {
		return err
	}
	s.usage += int64(len(data)) - replaced

	f, err := os.OpenFile(filepath.Join(dayDir, indexName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	s.usage += int64(len(line))
	s.sig = dirSignature(s.dir)
	return nil
}

// Writable probes whether the spool can currently accept a write: the
// directory accepts new files and the quota is not exhausted. The probe
// file never becomes visible to readers.
func (s *Spool) Writable() error {
	s.mu.Lock()
	s.refreshLocked()
	// Room for even one more byte is the bar: the quota refuses when full.
	full := s.wouldExceedLocked(1)
	s.mu.Unlock()
	if full {
		return ErrQuotaExceeded
	}
	f, err := os.CreateTemp(s.dir, ".writable-*")
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(f.Name())
}

// rewriteIndexLocked drops removed request ids from a day index. A
// malformed line is kept as-is: the index is advisory and rebuildable,
// so losing it would be worse than carrying a stale line. The rewrite
// is a read-modify-write reachable from the resident proxy and from
// short-lived CLI processes at once, so it runs under fsatomic.Update
// rather than the in-process mutex alone.
func (s *Spool) rewriteIndexLocked(dayDir string, removed map[string]bool) error {
	path := filepath.Join(dayDir, indexName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var delta int64
	err := fsatomic.Update(path, 0o600, func(old []byte) ([]byte, error) {
		var kept []byte
		for _, line := range bytes.Split(old, []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var rec indexLine
			if err := json.Unmarshal(line, &rec); err == nil && removed[rec.RequestID] {
				continue
			}
			kept = append(kept, line...)
			kept = append(kept, '\n')
		}
		delta = int64(len(kept)) - int64(len(old))
		return kept, nil
	})
	if err != nil {
		return err
	}
	s.usage += delta
	return nil
}

// Usage reports current spool size in bytes.
func (s *Spool) Usage() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.usage
}

// Quota reports the configured limit in bytes.
func (s *Spool) Quota() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quota
}

// SetQuota adjusts the limit for subsequent writes; the service tunes
// it through the upload handshake. A non-positive quota is ignored:
// nothing may turn the bound off, and shrinking below current usage
// only stops recording — it never evicts what was captured.
func (s *Spool) SetQuota(quota int64) {
	if quota <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quota = quota
}
