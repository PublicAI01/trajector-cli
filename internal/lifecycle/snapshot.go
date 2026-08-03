package lifecycle

import (
	"errors"
	"io/fs"
	"os"
)

// fileSnapshot remembers one file's exact pre-enable state so rollback
// can restore it byte for byte, including "did not exist".
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
		data, err := os.ReadFile(path)
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
			err = os.WriteFile(snap.path, snap.data, snap.mode)
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
