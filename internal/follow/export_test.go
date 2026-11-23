package follow

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
)

// SetEntryLockWait makes every reader in this test binary give up on
// an entry another reader holds after wait, until t ends.
func SetEntryLockWait(t testing.TB, wait time.Duration) {
	t.Helper()
	was := entryLockWait
	entryLockWait = wait
	t.Cleanup(func() { entryLockWait = was })
}

// MoveCursor writes to onto the entry registered for to.Path, from the
// cursor from, as a reader that holds the entry writes it.
func MoveCursor(r *Registry, projectIDHash string, from, to File) error {
	held, ok := r.lockEntry(projectIDHash, to.Path, entryLockWait)
	if !ok {
		return errors.New("follow: the entry could not be locked")
	}
	defer held.unlock()
	return held.advance(from, to, drift.Signals{})
}

// DieAfterMigrationStep has this process killed as soon as step of a
// migration is done, as a process killed between two steps is.
func DieAfterMigrationStep(step int) {
	afterMigrationStep = func(done int) {
		if done != step {
			return
		}
		self, err := os.FindProcess(os.Getpid())
		if err == nil {
			_ = self.Kill()
		}
		select {}
	}
}

// BeforePruneLocks has every Prune in this test binary call at with the
// path of each entry it would remove, just before it takes the entry's
// lock, until t ends.
func BeforePruneLocks(t testing.TB, at func(path string)) {
	t.Helper()
	was := beforePruneLock
	beforePruneLock = at
	t.Cleanup(func() { beforePruneLock = was })
}

// AfterMigrationSteps has every migration in this test binary call at
// with the number of each step it has done, until t ends.
func AfterMigrationSteps(t testing.TB, at func(step int)) {
	t.Helper()
	was := afterMigrationStep
	afterMigrationStep = at
	t.Cleanup(func() { afterMigrationStep = was })
}
