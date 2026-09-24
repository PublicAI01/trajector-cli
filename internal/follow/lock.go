package follow

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// entryLockStale bounds the lock one round of reading an entry holds.
// It is longer than the slowest round: one segment read — at most one
// pass over the rest of the file to find a relocated line — and
// stored, where a segment may be one line of maxLineBytes read,
// redacted, and written to disk. A lock older than that is taken to be
// one a reader left when it died, and a reader in another process takes
// the entry from it. The age is read off the wall clock and the lock
// is not renewed while it is held, so a system suspended past the bound
// in the middle of a round makes a live holder look that old too: two
// readers then read from one cursor again, and lines only the one whose
// segment the store discards observed can be passed. That is accepted:
// it takes a suspension inside a round while a reader in another
// process is waiting.
const entryLockStale = 2 * time.Minute

// entryLockWait is how long a reader waits for an entry another reader
// holds. It is short against both the stale bound and the time a
// session hook waits for a read it asked for: a reader that finds the
// entry held leaves it to the holder. Only tests change it.
var entryLockWait = 2 * time.Second

// lockEntry takes the lock that makes one read of path — read, store,
// write the cursor back — the only one on this device. ok is false
// when another reader still holds the entry after wait. unlock lets
// the entry go, and reports whether a reader in this process gave up
// waiting for it while it was held.
//
// Readers in one process wait on each other here before they contend
// for the lock file, so a live reader in this process is never judged
// stale by another one in it. The lock file is what readers in other
// processes contend for.
func (r *Registry) lockEntry(projectIDHash, path string, wait time.Duration) (unlock func() (abandoned bool), ok bool) {
	deadline := time.Now().Add(wait)
	target := r.entryLockPath(projectIDHash, path)
	release, ok := inProcess.lock(target, deadline)
	if !ok {
		return nil, false
	}
	unlockFile, err := fsatomic.Lock(target, entryLockStale, time.Until(deadline))
	if err != nil {
		release()
		return nil, false
	}
	return func() bool {
		unlockFile()
		return release()
	}, true
}

// entryLockPath names the lock of one entry beside its project's
// registry. The name holds a digest of the path and not the path, so
// the directory listing tells no more than the registry does.
func (r *Registry) entryLockPath(projectIDHash, path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(r.dir, projectIDHash+"."+hex.EncodeToString(sum[:16]))
}

// inProcess holds the entries a reader in this process is reading now.
var inProcess = entryLocks{held: map[string]*holding{}}

// entryLocks is a set of named locks whose waiters give up at a
// deadline. A name is in held while its lock is taken; its channel is
// closed on release, which wakes every waiter to try again.
type entryLocks struct {
	mu   sync.Mutex
	held map[string]*holding
}

// holding is one taking of a named lock.
type holding struct {
	released chan struct{}
	// abandoned records that a waiter gave up on this taking. It is
	// set and read under the set's mu, and read as the name leaves
	// held, so a waiter either marks the taking its holder is about to
	// let go of, or finds the name free and takes it: none gives up
	// unseen.
	abandoned bool
}

// lock takes name, waiting until deadline for whoever holds it. unlock
// lets name go and reports whether a waiter gave up on it meanwhile.
func (l *entryLocks) lock(name string, deadline time.Time) (unlock func() (abandoned bool), ok bool) {
	for {
		l.mu.Lock()
		cur, busy := l.held[name]
		if !busy {
			mine := &holding{released: make(chan struct{})}
			l.held[name] = mine
			l.mu.Unlock()
			return func() bool {
				l.mu.Lock()
				delete(l.held, name)
				abandoned := mine.abandoned
				l.mu.Unlock()
				close(mine.released)
				return abandoned
			}, true
		}
		l.mu.Unlock()
		select {
		case <-cur.released:
		case <-time.After(time.Until(deadline)):
			l.mu.Lock()
			if l.held[name] == cur {
				cur.abandoned = true
				l.mu.Unlock()
				return nil, false
			}
			l.mu.Unlock()
		}
	}
}
