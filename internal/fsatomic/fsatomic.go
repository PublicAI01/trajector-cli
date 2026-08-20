// Package fsatomic owns the read and write technique for shared state
// files. A reader of a path observes either the previous content or the
// new content in full, never a torn write; an Update additionally
// observes every earlier Update, so read-modify-write callers in
// separate processes never lose each other's changes.
//
// Reads of a path that WriteFile replaces must come through ReadFile:
// on Windows, an os.ReadFile crossing the replacing rename fails with a
// sharing violation, and its open handle in turn makes the rename fail
// with a permission error until it closes.
package fsatomic

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sibling-name markers. Every transient file this package creates next
// to a path spells the path's own name first and one of these after it,
// so a transient name never ends in the path's real extension and a
// directory scan selecting on that extension can never pick one up.
const (
	tempMarker  = ".tmp-"
	lockSuffix  = ".lock"
	ejectSuffix = ".eject"
)

// Replacement collision parameters. The colliding holds are an open
// handle for the microseconds a read takes, or an in-flight rename;
// both clear far inside one retry step. The window is generous so a
// slower holder — an antivirus scanning the fresh file — also clears.
const (
	replaceRetry  = 5 * time.Millisecond
	replaceWindow = 500 * time.Millisecond
)

// WriteFile writes data to path through a uniquely named same-directory
// temp file and one rename. Concurrent writers of one path never touch
// each other's temp files, and each rename installs one writer's
// complete data, the last one landing final — so plain writers may
// race, but a read-modify-write cycle still needs Update to not lose
// the writes that land between its read and its rename. A writer that
// dies before its rename leaves its temp file behind: no reader ever
// opens one (see the marker contract above), and Update sweeps aged
// ones for the paths it manages.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	tmp, err := writeTemp(path, data, perm)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(replaceWindow)
	for {
		err := renameReplace(tmp, path)
		if err == nil {
			return nil
		}
		if !replaceCollision(err) || time.Now().After(deadline) {
			os.Remove(tmp)
			return err
		}
		time.Sleep(replaceRetry)
	}
}

// syncFile flushes a temp file's content to stable storage before it is
// renamed into place. It is a variable only so a test can observe that
// the flush happens and that a failed one aborts the write; production
// always uses (*os.File).Sync.
var syncFile = (*os.File).Sync

// writeTemp creates path's uniquely named temp sibling holding data.
// The temp is born owner-only and widened to perm only once its content
// is complete; the explicit chmod applies perm exactly, so a mode taken
// from the file being replaced is preserved rather than re-masked.
//
// The content is flushed before writeTemp returns, so the rename that
// follows can never outlive the data it installs. Without the flush,
// rename only orders a directory entry: a crash between the two leaves
// the entry pointing at blocks that were never written, and the file
// reads back empty — with the previous content already gone. The paths
// this package replaces include the user's own Claude Code settings, so
// the failure is silent and unrecoverable rather than merely annoying:
// parsing an empty settings file succeeds and yields no settings at
// all. "Never a torn write" has to hold across a crash, not only across
// a concurrent reader. 2026-08-20.
func writeTemp(path string, data []byte, perm fs.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+tempMarker+"*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = syncFile(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// ReadFile reads path tolerating a concurrent WriteFile. The handle is
// opened with delete sharing so it never blocks the replacing rename,
// and an open that crosses the rename itself is retried within a
// bounded window rather than surfaced as a spurious failure.
func ReadFile(path string) ([]byte, error) {
	deadline := time.Now().Add(replaceWindow)
	for {
		data, err := readOnce(path)
		if err == nil || !replaceCollision(err) || time.Now().After(deadline) {
			return data, err
		}
		time.Sleep(replaceRetry)
	}
}

func readOnce(path string) ([]byte, error) {
	f, err := openShared(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// Lock acquisition parameters. Holders are short-lived CLI mutations;
// a lock older than lockStale can only belong to a process that died
// without unlocking, and is ejected rather than waited out forever.
const (
	lockRetry   = 5 * time.Millisecond
	lockStale   = 10 * time.Second
	lockTimeout = 30 * time.Second
)

// Update rewrites path through fn while holding a same-directory lock
// file, so concurrent updaters — including ones in other processes —
// are serialized and none loses another's change. fn receives the
// current content, nil when the file does not exist yet, and returns
// the full replacement. The path's directory must already exist. Aged
// leftovers of writers that died mid-flight are swept here, under the
// lock, where no live writer's transient can be mistaken for one.
func Update(path string, perm fs.FileMode, fn func(old []byte) ([]byte, error)) error {
	unlock, err := lock(path + lockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	sweepOrphans(path)

	old, err := ReadFile(path)
	if os.IsNotExist(err) {
		old = nil
	} else if err != nil {
		return err
	}
	data, err := fn(old)
	if err != nil {
		return err
	}
	return WriteFile(path, data, perm)
}

// sweepOrphans removes what a writer that died mid-flight left next to
// path: temp files never renamed into place and ejection tickets never
// removed. It runs only while path's lock is held, and touches only
// names aged past the lock's own stale bound, so the transients of a
// live writer — which exist for milliseconds — are never in reach.
func sweepOrphans(path string) {
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	base := filepath.Base(path)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base+tempMarker) &&
			name != base+lockSuffix+ejectSuffix {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) <= lockStale {
			continue
		}
		os.Remove(filepath.Join(dir, name))
	}
}

// lock takes an exclusive lock via create-exclusive of a lock file,
// which every platform this runs on supports over local filesystems.
// The file carries a random owner mark: a lock ejected as stale may
// already have been recreated by the next holder, so release must
// recognize — and spare — an incarnation it did not create.
func lock(path string) (unlock func(), err error) {
	var mark [16]byte
	if _, err := rand.Read(mark[:]); err != nil {
		return nil, err
	}
	owner := []byte(hex.EncodeToString(mark[:]))
	deadline := time.Now().Add(lockTimeout)
	for {
		err := claimLock(path, owner)
		if err == nil {
			return func() { releaseLock(path, owner) }, nil
		}
		// Windows reports create-exclusive against a lock file whose
		// holder is unlocking as a permission error rather than an
		// existence one: the name stays visible while the delete is
		// pending. Both mean the same thing here — someone else has it —
		// and treating only one as contention makes an ordinary handoff
		// fail outright instead of waiting the few milliseconds it takes.
		if !os.IsExist(err) && !os.IsPermission(err) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > lockStale {
			// Whether or not this waiter won the ejection, the claim retry
			// goes through the shared deadline and pacing below: a lock
			// that stays stale because its ejection ticket is blocked must
			// still time out rather than spin.
			ejectStaleLock(path)
		}
		if time.Now().After(deadline) {
			// A directory this process genuinely cannot write reaches
			// the deadline the same way a held lock does, so the reason
			// travels with it rather than being guessed from the path.
			return nil, fmt.Errorf("fsatomic: %s held past the stale deadline: %w", path, err)
		}
		time.Sleep(lockRetry)
	}
}

// claimLock create-exclusively takes the lock file and stamps it with
// this holder's owner mark. A file that could not take the complete
// mark is removed before anyone could judge it stale, so no other
// writer can be holding it.
func claimLock(path string, owner []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(owner)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return werr
	}
	return nil
}

// releaseLock removes the lock only while it still carries this
// holder's mark: after a stale ejection the same path may hold the next
// writer's lock, which an outlived holder's release must leave
// standing. The check narrows the misfire window from the whole hold
// duration to the moment between the read and the remove.
func releaseLock(path string, owner []byte) {
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, owner) {
		return
	}
	os.Remove(path)
}

// ejectStaleLock removes a lock whose holder died without releasing it.
// The removal runs under a create-exclusive ejection ticket and only
// after re-judging the lock's age there: with one ejector at a time, a
// still-stale file is still the incarnation that was judged — a fresh
// lock can only be created at a name the stale file has left, and only
// this ejector removes it. Without the ticket, a slower waiter working
// from a pre-ejection judgment would remove the fresh lock that the
// winner's ejection plus a new claim just put at the same path. Waiters
// that lose the ticket re-enter the wait loop and judge afresh.
func ejectStaleLock(path string) {
	ticket := path + ejectSuffix
	f, err := os.OpenFile(ticket, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		// A ticket aged past the stale bound belongs to an ejector that
		// died mid-ejection; left alone it would block ejection forever.
		if info, statErr := os.Stat(ticket); statErr == nil && time.Since(info.ModTime()) > lockStale {
			os.Remove(ticket)
		}
		return
	}
	f.Close()
	defer os.Remove(ticket)
	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > lockStale {
		os.Remove(path)
	}
}
