package follow

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
)

// gone reports whether the file at path is gone for good: the file is
// not there, and the directory that holds its session's main file is.
// It is the one judgment of that, for every caller that drops an entry
// for it: a reader, a prune and a migration. For a main file that directory is the one that held it; an
// agent file lies further down, in the session's own directory, which
// goes with the session when the session is removed. A directory of
// main files that is gone as well says nothing about the file: a
// volume that is not mounted, or a configuration directory moved
// away, takes that directory with it, and the file may come back with
// it. A file or a directory that cannot be looked at is still there.
func gone(path string) bool {
	info, err := os.Stat(filepath.Dir(SessionOf(path)))
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Lstat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// prunable reports whether f, with the heat of its session, is an
// entry nothing reads again: its session is cold at now and its file
// is gone. A hot entry is left to its reader, which drops it once it
// finds the file gone. An entry whose file is still there, or only out
// of reach, is never dropped: a hook that names the file again would register it from
// byte 0, and the segments read from there would take indexes already
// sent with other lines.
func (f File) prunable(now time.Time, alive func(pid int) bool) bool {
	return !f.Hot(now, alive) && gone(f.Path)
}

// beforePruneLock runs as Prune is about to take the lock of an entry
// it would remove. It is a variable only so a test can change the
// entry in between, as another process may.
var beforePruneLock = func(string) {}

// Prune removes from projectIDHash's registry every entry nothing reads
// again — its session cold at now, its file gone — and reports how
// many it removed. alive answers whether a process id names a running
// process. An entry that needs what this build does not know, and an
// entry file that does not parse, are left as they are. What the
// removed entries noticed is kept in the project file, in one write
// for them all, before any of them is removed. A session whose last
// entry is removed loses its heat and its told mark with it.
//
// Each entry is judged again under its lock, since its file may have
// come back or its session been named meanwhile, and one another
// reader holds is left to the next prune. Prune is for a process that
// is in no hurry; a project in the layout before this one is refused
// with ErrMigrationPending, as every reading of it is.
func (r *Registry) Prune(projectIDHash string, now time.Time, alive func(pid int) bool) (removed int, err error) {
	_, entries, _, err := r.listAll(projectIDHash)
	if err != nil {
		return 0, err
	}
	heats := map[string]heat{}
	var held []*heldEntry
	defer func() {
		for _, h := range held {
			h.unlock()
		}
	}()
	var noticed *drift.Signals
	for _, e := range entries {
		if !e.readable() || !r.warmed(projectIDHash, e.File, heats).prunable(now, alive) {
			continue
		}
		beforePruneLock(e.Path)
		h, ok := r.lockEntry(projectIDHash, e.Path, 0)
		if !ok {
			continue
		}
		e, ok, err := h.read()
		if err != nil || !ok || !e.readable() || !r.warmed(projectIDHash, e.File, nil).prunable(now, alive) {
			h.unlock()
			continue
		}
		held = append(held, h)
		if e.Signals != nil {
			noticed = plus(noticed, *e.Signals)
		}
	}
	if noticed != nil {
		err := r.updateProject(projectIDHash, false, func(p *projectFile) error {
			p.Signals = plus(p.Signals, *noticed)
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	sessions := map[string]string{}
	for _, h := range held {
		if err := r.dropEntry(projectIDHash, h.path); err != nil {
			return removed, err
		}
		removed++
		sessions[SessionOf(h.path)] = h.path
	}
	if len(sessions) == 0 {
		return 0, nil
	}
	_, rest, _, err := r.listAll(projectIDHash)
	if err != nil {
		return removed, err
	}
	for _, e := range rest {
		delete(sessions, SessionOf(e.Path))
	}
	var errs []error
	for _, path := range sessions {
		errs = append(errs, r.dropSessionMarks(projectIDHash, path))
	}
	return removed, errors.Join(errs...)
}
