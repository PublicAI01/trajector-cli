package lifecycle

import (
	"errors"
	"io/fs"
	"os"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// fileSnapshot remembers one file's exact pre-enable state so rollback
// can restore it byte for byte, including "did not exist". Only files
// this process exclusively owns belong here — the project-local settings
// file, which trajector writes and nothing else does. State shared with
// concurrent processes is undone entry-wise through its own writer
// instead: the routing table and the consent store through their stores,
// the project's .gitignore through claudesettings.RemoveGitIgnored. That
// last one was in this list until 2026-08-27, which made a rolled-back
// enable rewrite a file it may never have touched.
type fileSnapshot struct {
	path    string
	data    []byte
	mode    fs.FileMode
	existed bool
}

type snapshots []fileSnapshot

func takeSnapshots(paths ...string) (snapshots, error) {
	var s snapshots
	for _, path := range paths {
		snap := fileSnapshot{path: path}
		data, err := fsatomic.ReadFile(path)
		switch {
		case err == nil:
			info, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			snap.data, snap.mode, snap.existed = data, info.Mode().Perm(), true
		case os.IsNotExist(err):
		default:
			return nil, err
		}
		s = append(s, snap)
	}
	return s, nil
}

// restore puts every snapshotted file back. It keeps going on error so
// one unwritable file cannot strand the others half-restored, and
// reports everything that failed.
func (s snapshots) restore() error {
	var errs []error
	for _, snap := range s {
		var err error
		if snap.existed {
			// Through fsatomic, not a plain truncating write: these are the
			// user's own project files, and a rollback that dies partway
			// through restoring one would leave it worse than the failure
			// being rolled back. It also matches how takeSnapshots read
			// them. 2026-08-15.
			err = fsatomic.WriteFile(snap.path, snap.data, snap.mode)
		} else {
			err = os.Remove(snap.path)
			if os.IsNotExist(err) {
				err = nil
			}
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
