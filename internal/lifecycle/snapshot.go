package lifecycle

import (
	"io/fs"
	"os"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// fileSnapshot remembers one file's exact pre-enable state so rollback
// can restore it byte for byte, including "did not exist". Only a file
// this process exclusively owns may be undone this way — the
// project-local settings file, which trajector writes and nothing else
// does. State shared with concurrent processes is undone entry-wise
// through its own writer instead: the routing table and the consent
// store through their stores, the project's .gitignore through
// claudesettings.RemoveGitIgnored. That last one was undone by
// byte-for-byte restore until 2026-08-27, which made a rolled-back
// enable rewrite a file it may never have touched. Both kinds of undo
// go on the same ledger, which replays them in reverse.
type fileSnapshot struct {
	path    string
	data    []byte
	mode    fs.FileMode
	existed bool
}

func takeSnapshot(path string) (fileSnapshot, error) {
	// restore renames through fsatomic, which would replace a symbolic
	// link rather than what it points at. Refusing stops the enable
	// before it changes anything.
	if err := claudesettings.RefuseSymlink(path); err != nil {
		return fileSnapshot{}, err
	}
	snap := fileSnapshot{path: path}
	data, err := fsatomic.ReadFile(path)
	switch {
	case err == nil:
		info, err := os.Stat(path)
		if err != nil {
			return fileSnapshot{}, err
		}
		snap.data, snap.mode, snap.existed = data, info.Mode().Perm(), true
	case os.IsNotExist(err):
	default:
		return fileSnapshot{}, err
	}
	return snap, nil
}

// restore puts the snapshotted file back.
func (s fileSnapshot) restore() error {
	if !s.existed {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	// Through fsatomic, not a plain truncating write: these are the
	// user's own project files, and a rollback that dies partway through
	// restoring one would leave it worse than the failure being rolled
	// back. It also matches how takeSnapshot read it. 2026-08-15.
	return fsatomic.WriteFile(s.path, s.data, s.mode)
}
