// Package spool stores captured records on disk until upload. It has
// three slots under one directory and one quota, which two of them
// count against. The layout is a documented product contract:
//
//	<dir>/<YYYYMMDD>/<request_id>.json                one serialized rawcall envelope
//	<dir>/<YYYYMMDD>/index.jsonl                      advisory sidecar index of that day
//	<dir>/records/<YYYYMMDD>/<record_id>.json         one serialized segment or snapshot
//	<dir>/records/<YYYYMMDD>/index.jsonl              advisory sidecar index of that day
//	<dir>/records-held/<YYYYMMDD>/<record_id>.json    one record kept on this machine
//
// No slot shares a directory with another: rawcall readers skip the
// subdirectories by name, and each other slot's readers begin inside
// its own. In every slot the files are the source of truth; where a
// slot keeps an index, that index only accelerates batching and
// deletion and can always be rebuilt by rescanning its day directory.
// Which directory a slot uses, whether it keeps an index, and whether
// its bytes count against the quota are stated once, in the slot
// table, so no walk decides any of the three by name. Directories are
// 0700 and files 0600: stored records hold unredacted data and must
// stay private to the user until masked and uploaded.
package spool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
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
//
// The slots the quota counts bear the refusal differently. A rawcall
// exists only in the moment it crosses the proxy: refused, it is
// gone. A segment is read from a file that stays on disk: refused,
// its reader leaves its position where it was and the segment is
// merely late. The spool refuses both all the same; what a caller does
// next is its own.
var ErrQuotaExceeded = errors.New("spool: quota exceeded")

// indexName is the per-day sidecar index file name.
const indexName = "index.jsonl"

// dayLayout names the day directory a record is stored under, in UTC,
// in every slot.
const dayLayout = "20060102"

// slot is one of the places the spool keeps records, and everything
// about the place that a walk would otherwise have to know by name:
// where its day directories are, whether it keeps a sidecar index
// beside them, and whether what it holds is charged to the quota.
type slot struct {
	// dir is the slot's directory under the spool root. The rawcall
	// slot names none: its day directories are the root's own children,
	// which is why every other slot's directory has to be skipped
	// wherever the root is listed.
	dir string
	// indexed says the slot keeps a per-day sidecar index. A slot
	// without one attributes every record from its own bytes.
	indexed bool
	// counted says the slot's bytes are charged to the quota. What the
	// quota counts is what recording can still be stopped for; a slot
	// nothing uploads has no way to drain itself, so charging it would
	// let records this build cannot mask stop recording device-wide.
	counted bool
}

var (
	rawcallSlot = slot{indexed: true, counted: true}
	recordSlot  = slot{dir: recordsDirName, indexed: true, counted: true}
	heldSlot    = slot{dir: heldDirName}

	// slots is the whole table, and the only statement of which
	// directories the spool root holds. Nothing here is ever written
	// to after this package is loaded.
	slots = []slot{rawcallSlot, recordSlot, heldSlot}
)

// isSlotDir reports whether a directory of the spool root belongs to a
// slot of its own rather than being a day of rawcalls.
func isSlotDir(name string) bool {
	return slices.ContainsFunc(slots, func(sl slot) bool { return sl.dir != "" && sl.dir == name })
}

// root is where the slot's day directories live.
func (sl slot) root(dir string) string {
	if sl.dir == "" {
		return dir
	}
	return filepath.Join(dir, sl.dir)
}

// days lists a slot's day directories, oldest first. A slot that was
// never written to has none. The rawcall slot's days are the spool
// root's own children, so the directories of the other slots are
// skipped there.
func (s *Spool) slotDays(sl slot) ([]string, error) {
	root := sl.root(s.dir)
	entries, err := listDir(root)
	if err != nil {
		return nil, err
	}
	var days []string
	for _, e := range entries {
		if !e.IsDir() || (sl.dir == "" && isSlotDir(e.Name())) {
			continue
		}
		days = append(days, filepath.Join(root, e.Name()))
	}
	return days, nil
}

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
	s := &Spool{dir: dir, quota: quota}
	// Before the first usage figure is taken, so the walk does not adopt
	// bytes that are about to be reclaimed. Opening is also the moment
	// after a crash, which is the only thing that strands these.
	s.sweepStaleTempsLocked()
	s.sig = dirSignature(dir)
	usage, err := quotaUsage(dir)
	if err != nil {
		return nil, err
	}
	s.usage = usage
	return s, nil
}

// sweepStaleTempsLocked removes rawcall temps stranded by a writer that
// died between WriteFile's create and its rename — an OOM kill, a power
// loss, a pulled plug. Such a file holds a full unredacted rawcall.
//
// Until 2026-09-04 nothing ever removed one. fsatomic.Update's own sweep
// reaches only the paths this package rewrites under a lock, which means
// index.jsonl and never a rawcall; and every reader here filters on a
// ".json" extension that "<id>.json.tmp-NNN" does not have, since
// filepath.Ext returns ".tmp-NNN". walkUsage filters nothing, so the
// bytes were charged all the same. That split cost twice over: the
// strand was invisible to DeleteProject, leaving a withdrawn project's
// rawcall on disk after the user was told it was deleted, and it was
// charged against the quota forever, eventually refusing all recording
// while Summary reported bytes with no records to explain them.
//
// Only aged temps are taken, so a live writer's transient — which
// exists for milliseconds — is never in reach. And only names no reader
// would open: storableRequestID permits dots, so an id may legitimately
// contain ".tmp-", but such a record still ends in ".json". The filter
// here is exactly the inverse of rawcallFiles', which is the point —
// what readers cannot see is what nothing else will ever reclaim.
//
// Record files are written the same way and stranded the same way, so
// every slot is swept.
//
// Callers hold s.mu, or hold the spool before it is published.
func (s *Spool) sweepStaleTempsLocked() {
	for _, sl := range slots {
		days, err := s.slotDays(sl)
		if err != nil {
			return
		}
		for _, dayDir := range days {
			entries, err := listDir(dayDir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || name == indexName || filepath.Ext(name) == ".json" {
					continue
				}
				info, err := e.Info()
				if err != nil || !fsatomic.StaleTempName(name, info.ModTime()) {
					continue
				}
				os.Remove(filepath.Join(dayDir, name))
			}
		}
	}
}

// vanished reports that what was to be opened is already gone.
//
// A record, an index, or a whole day directory disappearing while a
// walk is in flight is ordinary, not damage: `trajector disable`
// deletes a project's records from another process, and the uploader's
// own DeleteWhere runs against a live spool. What is gone holds no
// record and costs no bytes, so every walk here skips it and completes
// over the rest; any other error is still the walk's failure.
//
// The rule has one executor per step a walk takes to reach disk —
// listDir and openRecordFile — so a walk cannot hold an opinion of its
// own about it.
func vanished(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// listDir lists one directory of the spool. A directory that is not
// there lists as empty: it either was never written or has just been
// deleted, and a walk treats the two the same.
func listDir(dir string) ([]fs.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if vanished(err) {
		return nil, nil
	}
	return entries, err
}

// openRecordFile reads one file a walk visits. It reports false for a
// file that is already gone, which is the walk's signal to skip it, and
// an error only for a file that is there and cannot be read.
func openRecordFile(path string) ([]byte, bool, error) {
	data, err := fsatomic.ReadFile(path)
	if vanished(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// quotaUsage derives the spool size the quota is measured against: the
// slots the table marks counted, and no other. The held slot is left
// out because nothing uploads it — the records there leave only when a
// later build can mask them or the user deletes them — so charging
// them would let a shape this build cannot read stop all recording.
func quotaUsage(dir string) (int64, error) {
	var skip []string
	for _, sl := range slots {
		if !sl.counted {
			skip = append(skip, sl.root(dir))
		}
	}
	return walkUsage(dir, skip...)
}

// walkUsage derives total size under dir from disk, the authority the
// in-memory figure must always converge on. A directory that does not
// exist yet reads as nothing stored: WalkDir reports it through the
// same callback a vanished record arrives on. The directories named in
// skip are not descended into: their bytes are on disk all the same,
// and are simply not part of the question being asked.
func walkUsage(dir string, skip ...string) (int64, error) {
	var usage int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if vanished(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if slices.Contains(skip, path) {
				return fs.SkipDir
			}
			return nil
		}
		// DirEntry.Info lstats lazily, so a file listed a moment ago and
		// deleted since fails here rather than above.
		info, err := d.Info()
		if err != nil {
			if vanished(err) {
				return nil
			}
			return err
		}
		usage += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return usage, nil
}

// dirSignature fingerprints the day directories of every slot by name
// and mtime. It is deliberately cheap — one directory listing per slot
// plus one stat per day — so quota decisions can verify it without
// walking every record. A slot's own directory is listed rather than
// stamped: a write inside one of its days changes that day's mtime,
// not its own.
func dirSignature(dir string) string {
	var b []byte
	for _, sl := range slots {
		entries, err := os.ReadDir(sl.root(dir))
		if err != nil {
			continue
		}
		prefix := ""
		if sl.dir != "" {
			prefix = sl.dir + "/"
		}
		b = appendDaySignatures(b, prefix, entries)
	}
	return string(b)
}

func appendDaySignatures(b []byte, prefix string, entries []fs.DirEntry) []byte {
	for _, e := range entries {
		if !e.IsDir() || isSlotDir(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		b = fmt.Appendf(b, "%s%s/%d;", prefix, e.Name(), info.ModTime().UnixNano())
	}
	return b
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
	usage, err := quotaUsage(s.dir)
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

	day := at.UTC().Format(dayLayout)
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

// rewriteIndexLocked drops removed request ids from a rawcall day
// index.
func (s *Spool) rewriteIndexLocked(dayDir string, removed map[string]bool) error {
	return s.rewriteIndexFileLocked(filepath.Join(dayDir, indexName), func(line []byte) bool {
		var rec indexLine
		return json.Unmarshal(line, &rec) == nil && removed[rec.RequestID]
	})
}

// rewriteIndexFileLocked drops the lines of a sidecar index that drop
// accepts. A malformed line is kept as-is: the index is advisory and
// rebuildable, so losing it would be worse than carrying a stale line.
// The rewrite is a read-modify-write reachable from the resident proxy
// and from short-lived CLI processes at once, so it runs under
// fsatomic.Update rather than the in-process mutex alone.
func (s *Spool) rewriteIndexFileLocked(path string, drop func(line []byte) bool) error {
	if _, err := os.Stat(path); vanished(err) {
		return nil
	} else if err != nil {
		return err
	}
	var delta int64
	err := fsatomic.Update(path, 0o600, func(old []byte) ([]byte, error) {
		var kept []byte
		for _, line := range bytes.Split(old, []byte("\n")) {
			if len(line) == 0 || drop(line) {
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

// Count is how many stored records of each kind a spool holds, or one
// day of it. One value counts every kind, so nothing that reports what
// is waiting has to know how many kinds there are: the kinds are
// envelope's, and a kind it does not declare is counted under none of
// them.
type Count map[envelope.Kind]int

// Add counts one record of the kind it declares. A kind this client
// does not store is counted under no key, so a count never claims a
// record it cannot name.
func (c *Count) Add(kind envelope.Kind) {
	if kind.CountKey() == "" {
		return
	}
	if *c == nil {
		*c = Count{}
	}
	(*c)[kind]++
}

// Plus adds another count into this one.
func (c *Count) Plus(other Count) {
	if len(other) == 0 {
		return
	}
	if *c == nil {
		*c = Count{}
	}
	for kind, n := range other {
		(*c)[kind] += n
	}
}

// Total counts the records of every kind.
func (c Count) Total() int {
	total := 0
	for _, n := range c {
		total += n
	}
	return total
}

// DaySummary reports one day of the spool: counts and sizes only, never
// file names — ids belong to the records, not to diagnostics. The
// rawcalls it counts, with Bytes, describe the rawcall slot's day
// directory; every other kind it counts, with RecordBytes, describes
// the same day in the record slot. A day appears when either slot
// holds it.
type DaySummary struct {
	Day string `json:"day"`
	Count
	Bytes       int64 `json:"bytes"`
	RecordBytes int64 `json:"record_bytes"`
}

// MarshalJSON writes the day with one key per record kind, under the
// name that kind's count travels by and in envelope's order. The count
// keys are written here rather than carried by struct tags because the
// set of keys is the set of kinds: a kind added to envelope's table
// reaches this diagnostic with no edit here, and a key already written
// is never respelled.
func (d DaySummary) MarshalJSON() ([]byte, error) {
	day, err := json.Marshal(d.Day)
	if err != nil {
		return nil, err
	}
	out := append([]byte(`{"day":`), day...)
	for _, kind := range envelope.Kinds() {
		out = fmt.Appendf(out, `,%q:%d`, kind.CountKey(), d.Count[kind])
	}
	return fmt.Appendf(out, `,"bytes":%d,"record_bytes":%d}`, d.Bytes, d.RecordBytes), nil
}

// Summary walks the day directories of the slots the quota counts and
// reports each day. It reads the same tree Usage derives from, so the
// two can never disagree about what is on disk: the day sizes sum to
// Usage. The held slot is in neither figure — it is charged to no
// quota and waits for no upload, so what it holds is reported as what
// it is and not as part of the wait.
func (s *Spool) Summary() ([]DaySummary, error) {
	byDay := map[string]*DaySummary{}
	dayOf := func(dayDir string) *DaySummary {
		name := filepath.Base(dayDir)
		if d, ok := byDay[name]; ok {
			return d
		}
		d := &DaySummary{Day: name}
		byDay[name] = d
		return d
	}

	days, err := s.slotDays(rawcallSlot)
	if err != nil {
		return nil, err
	}
	for _, dayDir := range days {
		d := dayOf(dayDir)
		files, err := listDir(dayDir)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			if info, err := f.Info(); err == nil {
				d.Bytes += info.Size()
			}
			if filepath.Ext(f.Name()) == ".json" {
				d.Add(envelope.KindRawcall)
			}
		}
	}

	recordDays, err := s.slotDays(recordSlot)
	if err != nil {
		return nil, err
	}
	for _, dayDir := range recordDays {
		d := dayOf(dayDir)
		if err := summarizeRecordDay(dayDir, d); err != nil {
			return nil, err
		}
	}

	out := make([]DaySummary, 0, len(byDay))
	for _, d := range byDay {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}

// summarizeRecordDay counts a record day by kind. The index says which
// kind each file is; a file the index missed is described from its own
// bytes, and one that describes itself as nothing known is counted in
// the bytes and in neither kind.
func summarizeRecordDay(dayDir string, d *DaySummary) error {
	entries, err := listDir(dayDir)
	if err != nil {
		return err
	}
	for _, f := range entries {
		if f.IsDir() {
			continue
		}
		if info, err := f.Info(); err == nil {
			d.RecordBytes += info.Size()
		}
	}
	indexed, err := readRecordIndex(dayDir)
	if err != nil {
		return err
	}
	files, err := recordFiles(dayDir)
	if err != nil {
		return err
	}
	for _, f := range files {
		r, ok, err := recordFromIndex(f, indexed, func() ([]byte, bool, error) { return openRecordFile(f.path) })
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		d.Add(r.kind())
	}
	return nil
}

// Usage reports the spool size the quota is measured against, in
// bytes.
func (s *Spool) Usage() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.usage
}

// RecordsUsage reports how many bytes the second slot holds: the
// records read from session files and the observations made beside
// them. It is read from disk each time, as the whole-spool figure is
// re-derived, because a threshold that reads it is checked once a
// minute and never on a write.
func (s *Spool) RecordsUsage() int64 {
	usage, err := walkUsage(recordSlot.root(s.dir))
	if err != nil {
		return 0
	}
	return usage
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
