package follow

import (
	"testing"
	"time"
)

// SetEntryLockWait makes every reader in this test binary give up on
// an entry another reader holds after wait, until t ends.
func SetEntryLockWait(t testing.TB, wait time.Duration) {
	was := entryLockWait
	entryLockWait = wait
	t.Cleanup(func() { entryLockWait = was })
}
