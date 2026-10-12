package selfupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// lockStale is how old the upgrade lock has to be before it is taken to
// belong to an upgrade that died holding it. A live holder finishes well
// inside it: the archive download is bounded by downloadTimeout and the
// run of the staged binary by smokeTimeout, and nothing else in a run
// waits on anything slower than the release index.
const lockStale = 15 * time.Minute

// takeLock takes the upgrade lock at path, waiting up to wait for an
// upgrade that holds it. It returns ErrBusy when that upgrade still
// holds it at the end of the wait. The state directory may not exist
// yet on a machine that has never upgraded, and the lock lives there.
func takeLock(path string, wait time.Duration) (unlock func(), err error) {
	if err := userdirs.EnsureOwnerDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("selfupdate: %w", err)
	}
	unlock, err = fsatomic.Lock(path, lockStale, wait)
	if errors.Is(err, fs.ErrExist) && wait == 0 {
		// A claim that finds a stale lock ejects it and then judges its
		// deadline, which a zero wait has already passed: without a
		// second claim, a run that does not wait would give up on the
		// very lock it just cleared, and only the run after it would
		// get through.
		unlock, err = fsatomic.Lock(path, lockStale, 0)
	}
	if errors.Is(err, fs.ErrExist) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, fmt.Errorf("selfupdate: taking the upgrade lock: %w", err)
	}
	return unlock, nil
}
