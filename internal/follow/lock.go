package follow

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// entryLockStale bounds the lock one read of an entry holds. It is
// longer than the slowest read of one segment and the storing of it —
// a line of maxLineBytes read, redacted, and written to disk — so a
// lock that old can only belong to a reader that died.
const entryLockStale = 2 * time.Minute

// entryLockWait is how long a reader waits for an entry another reader
// holds. It is short against both the stale bound and the time a
// session hook waits for a read it asked for: a reader that finds the
// entry held leaves it to the holder, and the next run reads what is
// left. Only tests change it.
var entryLockWait = 2 * time.Second

// lockEntry takes the lock that makes one read of path — read, store,
// write the cursor back — the only one on this device. ok is false
// when another reader still holds the entry after wait.
//
// Readers in one process wait on each other here before they contend
// for the lock file, so a live reader in this process is never judged
// stale by another one in it. The lock file is what readers in other
// processes contend for.
func (r *Registry) lockEntry(projectIDHash, path string, wait time.Duration) (unlock func(), ok bool) {
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
	return func() {
		unlockFile()
		release()
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
var inProcess = entryLocks{held: map[string]chan struct{}{}}

// entryLocks is a set of named locks whose waiters give up at a
// deadline. A name is in held while its lock is taken; its channel is
// closed on release, which wakes every waiter to try again.
type entryLocks struct {
	mu   sync.Mutex
	held map[string]chan struct{}
}

func (l *entryLocks) lock(name string, deadline time.Time) (unlock func(), ok bool) {
	for {
		l.mu.Lock()
		released, busy := l.held[name]
		if !busy {
			mine := make(chan struct{})
			l.held[name] = mine
			l.mu.Unlock()
			return func() {
				l.mu.Lock()
				delete(l.held, name)
				l.mu.Unlock()
				close(mine)
			}, true
		}
		l.mu.Unlock()
		select {
		case <-released:
		case <-time.After(time.Until(deadline)):
			return nil, false
		}
	}
}
