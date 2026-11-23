package follow

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// entryFile is one entry as its file holds it: the entry, what reading
// the file noticed, the bound of a read whose cursor is not written
// yet, and what a build must know to read the entry.
type entryFile struct {
	File
	Signals *drift.Signals `json:"signals,omitempty"`
	Bound   *segmentBound  `json:"bound,omitempty"`
	Needs   []string       `json:"needs,omitempty"`
	// Unknown holds the members this build does not know, written back
	// as they were found.
	Unknown jsontext.Value `json:",embed"`
}

// readable reports whether this build may read e. It knows none of the
// names an entry can need.
func (e entryFile) readable() bool { return len(e.Needs) == 0 }

// standingBound is the bound e holds while it still stands for the
// cursor e holds.
func (e entryFile) standingBound() (b segmentBound, ok bool) {
	if e.Bound == nil {
		return segmentBound{}, false
	}
	return *e.Bound, e.Bound.standsFor(e.File)
}

func (r *Registry) entryPath(projectIDHash, path string) string {
	return filepath.Join(r.projectDir(projectIDHash), digest(path)+jsonExt)
}

// readEntry is the entry registered for path, whatever it needs. ok is
// false for a path that is not registered.
func (r *Registry) readEntry(projectIDHash, path string) (e entryFile, ok bool, err error) {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return entryFile{}, false, err
	}
	e, err = readEntryFile(r.entryPath(projectIDHash, path))
	if errors.Is(err, fs.ErrNotExist) {
		return entryFile{}, false, nil
	}
	return e, err == nil, err
}

func readEntryFile(file string) (entryFile, error) {
	data, err := fsatomic.ReadFile(file)
	if err != nil {
		return entryFile{}, err
	}
	var e entryFile
	if err := json.Unmarshal(data, &e); err != nil {
		return entryFile{}, fmt.Errorf("follow: entry %s: %w", filepath.Base(file), err)
	}
	return e, nil
}

// create writes e as a new entry, and leaves an entry that is there
// already as it is, readable or not: a registration never replaces an
// entry.
func (r *Registry) create(projectIDHash string, e entryFile) error {
	target := r.entryPath(projectIDHash, e.Path)
	if _, err := os.Lstat(target); err == nil {
		return nil
	}
	data, err := encode(e)
	if err != nil {
		return err
	}
	return placeNew(target, data, true)
}

// placeNew puts data at target unless a file stands there already. The
// file is staged under another name and linked into place, so it
// appears whole or not at all, and the link fails rather than replace a
// file another writer put there first. flush makes the content durable
// before it is linked; a caller that flushes many files at once later
// leaves it unset.
func placeNew(target string, data []byte, flush bool) error {
	if _, err := os.Lstat(target); err == nil {
		return nil
	}
	staged, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".new-*")
	if err != nil {
		return err
	}
	_, werr := staged.Write(data)
	if werr == nil && flush {
		werr = staged.Sync()
	}
	if cerr := staged.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Link(staged.Name(), target)
		if errors.Is(werr, fs.ErrExist) {
			werr = nil
		}
	}
	_ = os.Remove(staged.Name())
	return werr
}

// heldEntry is one entry whose lock this process holds. Writing an
// entry that exists goes through it alone: only the reader that holds
// an entry moves it.
type heldEntry struct {
	registry      *Registry
	projectIDHash string
	path          string
	release       func() (abandoned bool)
}

// unlock lets the entry go, and reports whether a reader in this
// process gave up waiting for it while it was held.
func (h *heldEntry) unlock() bool { return h.release() }

func (h *heldEntry) read() (entryFile, bool, error) {
	return h.registry.readEntry(h.projectIDHash, h.path)
}

// write rewrites the entry with change applied. The members this build
// does not know are written back as found. An entry removed in the
// meantime stays removed, and one that needs what this build does not
// know is never rewritten.
func (h *heldEntry) write(change func(*entryFile) error) error {
	e, ok, err := h.read()
	if err != nil {
		return err
	}
	if !ok || !e.readable() {
		return ErrNotRegistered
	}
	if err := change(&e); err != nil {
		return err
	}
	data, err := encode(e)
	if err != nil {
		return err
	}
	return fsatomic.WriteFile(h.registry.entryPath(h.projectIDHash, h.path), data, 0o600)
}

// keepBound writes b as the bound of the read in flight, and leaves the
// cursor where it is.
func (h *heldEntry) keepBound(b segmentBound) error {
	return h.write(func(e *entryFile) error {
		e.Bound = &b
		return nil
	})
}

// notice keeps what a read noticed and leaves the cursor where it is.
// It is for a read whose findings stop reading before any of its
// records is stored: no cursor write follows to carry them.
func (h *heldEntry) notice(noticed drift.Signals) error {
	return h.write(func(e *entryFile) error {
		e.Signals = plus(e.Signals, noticed)
		return nil
	})
}

// advance writes what reading reached — the fields a reader owns, as
// advancedTo names them — onto the entry, together with what that read
// noticed, and drops the bound: the cursor and the counts of the lines
// it passed land together or not at all. The entry is left as it is,
// and advance fails, when its Offset or NextSegment moved since from
// was read: to was made from a cursor that is no longer the entry's,
// and writing it would put the cursor past lines that no record of the
// other reader holds.
func (h *heldEntry) advance(from, to File, noticed drift.Signals) error {
	return h.write(func(e *entryFile) error {
		if e.Offset != from.Offset || e.NextSegment != from.NextSegment {
			return errCursorMoved
		}
		e.File = e.advancedTo(to)
		e.Signals = plus(e.Signals, noticed)
		e.Bound = nil
		return nil
	})
}
