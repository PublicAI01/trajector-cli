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

// Entry describes one rawcall being written.
type Entry struct {
	RequestID string
	// SessionKey groups records of the same coding session so upload
	// batching can lay them out adjacently. Empty when the request body
	// carried no session identity.
	SessionKey string
	// Timestamp selects the day directory.
	Timestamp time.Time
}

// indexLine is one record in the sidecar index.
type indexLine struct {
	RequestID  string `json:"request_id"`
	SessionKey string `json:"session_key,omitempty"`
	Timestamp  string `json:"timestamp"`
	Size       int64  `json:"size"`
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

// refreshLocked re-derives usage when another handle has changed the
// directory. A change that lands between this handle's own mutation and
// its signature snapshot is missed once, but the very next mutation by
// any handle changes the signature again, so usage always converges.
func (s *Spool) refreshLocked() {
	sig := dirSignature(s.dir)
	if sig == s.sig {
		return
	}
	usage, err := walkUsage(s.dir)
	if err != nil {
		return
	}
	s.usage = usage
	s.sig = sig
}

// Write stores one serialized envelope atomically and appends it to the
// day's index.
func (s *Spool) Write(e Entry, data []byte) error {
	// The spool builds a file path from the id and must not trust it.
	if !envelope.ValidRequestID(e.RequestID) {
		return fmt.Errorf("spool: invalid request id %q", e.RequestID)
	}
	line, err := json.Marshal(indexLine{
		RequestID:  e.RequestID,
		SessionKey: e.SessionKey,
		Timestamp:  e.Timestamp.UTC().Format(time.RFC3339Nano),
		Size:       int64(len(data)),
	})
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()

	day := e.Timestamp.UTC().Format("20060102")
	dayDir := filepath.Join(s.dir, day)
	final := filepath.Join(dayDir, e.RequestID+".json")
	var replaced int64
	if info, err := os.Stat(final); err == nil {
		replaced = info.Size()
	}
	if s.usage-replaced+int64(len(data))+int64(len(line)) > s.quota {
		return ErrQuotaExceeded
	}

	if err := os.MkdirAll(dayDir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dayDir, "."+e.RequestID+".json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
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
	usage, quota := s.usage, s.quota
	s.mu.Unlock()
	if usage >= quota {
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
// so losing it would be worse than carrying a stale line.
func (s *Spool) rewriteIndexLocked(dayDir string, removed map[string]bool) error {
	path := filepath.Join(dayDir, indexName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var kept []byte
	for _, line := range bytes.Split(data, []byte("\n")) {
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, kept, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	s.usage += int64(len(kept)) - int64(len(data))
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
