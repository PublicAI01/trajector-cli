// Package fsatomic owns the write technique for shared state files. A
// reader of a path observes either the previous content or the new
// content in full, never a torn write; an Update additionally observes
// every earlier Update, so read-modify-write callers in separate
// processes never lose each other's changes.
package fsatomic

import (
	"fmt"
	"io/fs"
	"os"
	"time"
)

// WriteFile writes data to path through a same-directory temp file and
// rename. The temp name is fixed (path + ".tmp"), so concurrent writers
// of one path must already be serialized by the caller; Update is the
// variant that serializes them itself.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
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
// the full replacement. The path's directory must already exist.
func Update(path string, perm fs.FileMode, fn func(old []byte) ([]byte, error)) error {
	unlock, err := lock(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	old, err := os.ReadFile(path)
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

// lock takes an exclusive lock via create-exclusive of a lock file,
// which every platform this runs on supports over local filesystems.
func lock(path string) (unlock func(), err error) {
	deadline := time.Now().Add(lockTimeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }, nil
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
			os.Remove(path)
			continue
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
