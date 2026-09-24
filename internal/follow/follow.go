// Package follow keeps the registry of files trajector reads for an
// enabled project, and how far each has been read. The layout is a
// documented product contract:
//
//	<dir>/<project-id-hash>.json            one registry per enabled project
//	<dir>/<project-id-hash>.<digest>.lock   held while one of its files is read
//
// A registry is one JSON object: {"version":1,"files":[...]}, each
// element carrying path, inode, size, offset, next_segment, and
// message_ids, and optionally read_at, subpath, retired, last_event,
// pid, told, and before_rewrite. A session that moved to a directory
// consent does not cover is written as retired "relocated", the form
// an earlier build wrote: an earlier build then never reads on past
// the line that took the session out. The path
// is the entry's identity. Inode, size, and offset describe the file as it
// was last observed locally: they steer reading and never leave it.
// An optional "gaps" object records what the one-time search for the
// project's earlier files could not cover, so a later reading of the
// registry can say so without searching again. An optional "signals"
// object accumulates what the reader noticed about the shape of the
// lines it read, as counts and field names only, so a surface can
// state it without opening a session file; it is kept in the form
// drift gives it and summed by drift's own arithmetic. Directories
// are 0700 and files 0600: the paths alone reveal what the user works
// on.
//
// Reading is a function of one registered entry and the file on disk:
// Read consumes the complete lines a file gained since its cursor and
// hands back the records to store together with the advanced cursor,
// and a Reader holds the whole rule around it — read, hand the records
// to a store, advance or drop the entry, persist. The registry
// decides nothing about content; nothing here writes a file it reads.
package follow

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// version is the registry layout written by this package. A registry
// carrying any other version is refused rather than interpreted: a
// cursor read under the wrong layout would silently skip or repeat
// content.
const version = 1

const registryExt = ".json"

// ErrNotRegistered reports an update to a path the project never
// registered, or to a project without a registry.
var ErrNotRegistered = errors.New("follow: file is not registered")

// errCursorMoved reports an update made from a cursor the entry no
// longer holds: another reader moved it in the meantime.
var errCursorMoved = errors.New("follow: cursor moved since it was read")

// Registry is the on-disk set of files trajector reads for an enabled
// project, and how far each has been read.
type Registry struct{ dir string }

// Open prepares the registry rooted at dir without creating anything.
// The directory is created, owner-only, by the first registration.
func Open(dir string) *Registry { return &Registry{dir: dir} }

type registry struct {
	Version int            `json:"version"`
	Files   []File         `json:"files"`
	Gaps    *Gaps          `json:"gaps,omitempty"`
	Signals *drift.Signals `json:"signals,omitempty"`
}

// Register adds path to projectIDHash's registry with a zero cursor and
// no session position. Register is RegisterUnder with an empty subpath.
func (r *Registry) Register(projectIDHash, path string) error {
	return r.RegisterUnder(projectIDHash, path, "")
}

// RegisterUnder adds path to projectIDHash's registry with a zero
// cursor, recording where the session ran relative to the project root.
// Registering a path that is already registered changes nothing, its
// subpath included: the first registration of a session's file is the
// one that knows where it ran. A retired entry is registered already,
// so registering its path again does not read the file over. The path
// must be absolute: it is the entry's identity across processes with
// different working directories.
func (r *Registry) RegisterUnder(projectIDHash, path, subpath string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("follow: path %q is not absolute", path)
	}
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return err
	}
	if err := userdirs.EnsureOwnerDir(r.dir); err != nil {
		return err
	}
	return fsatomic.Update(r.path(projectIDHash), 0o600, func(old []byte) ([]byte, error) {
		reg, err := parse(old)
		if err != nil {
			return nil, err
		}
		if _, ok := find(reg.Files, path); !ok {
			reg.Files = append(reg.Files, File{Path: path, Subpath: subpath})
		}
		return encode(reg)
	})
}

// SetGaps records what the latest search for projectIDHash's earlier
// files left uncovered, replacing what an earlier search recorded. A
// project with no registry gets one: a search that found nothing and
// covered nothing is still an outcome to keep.
func (r *Registry) SetGaps(projectIDHash string, gaps Gaps) error {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return err
	}
	if err := userdirs.EnsureOwnerDir(r.dir); err != nil {
		return err
	}
	return fsatomic.Update(r.path(projectIDHash), 0o600, func(old []byte) ([]byte, error) {
		reg, err := parse(old)
		if err != nil {
			return nil, err
		}
		reg.Gaps = nil
		if gaps.Any() {
			reg.Gaps = &gaps
		}
		return encode(reg)
	})
}

// Gaps reports what the latest search for projectIDHash's earlier
// files left uncovered. A project with no registry, or one whose
// search covered everything, has no gaps.
func (r *Registry) Gaps(projectIDHash string) (Gaps, error) {
	reg, err := r.read(projectIDHash)
	if err != nil {
		return Gaps{}, err
	}
	if reg.Gaps == nil {
		return Gaps{}, nil
	}
	return *reg.Gaps, nil
}

// AddSignals sums what one read noticed into projectIDHash's registry.
// A project with no registry gets one: what was noticed about a
// project's files outlives the entries for the files themselves. What
// a signal holds is drift's to name; the registry only keeps the sum.
func (r *Registry) AddSignals(projectIDHash string, more drift.Signals) error {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return err
	}
	if err := userdirs.EnsureOwnerDir(r.dir); err != nil {
		return err
	}
	return fsatomic.Update(r.path(projectIDHash), 0o600, func(old []byte) ([]byte, error) {
		reg, err := parse(old)
		if err != nil {
			return nil, err
		}
		var sum drift.Signals
		if reg.Signals != nil {
			sum = *reg.Signals
		}
		sum = sum.Add(more)
		reg.Signals = nil
		if sum.Any() {
			reg.Signals = &sum
		}
		return encode(reg)
	})
}

// Signals reports everything the reader noticed about projectIDHash's
// files so far. A project with no registry, or one whose reads noticed
// nothing, has none.
func (r *Registry) Signals(projectIDHash string) (drift.Signals, error) {
	reg, err := r.read(projectIDHash)
	if err != nil {
		return drift.Signals{}, err
	}
	if reg.Signals == nil {
		return drift.Signals{}, nil
	}
	return *reg.Signals, nil
}

// Files lists the files projectIDHash still reads, ordered by path. A
// project with no registry has no files. A retired entry is not
// listed: nothing is left to read of it, and it is kept only so that
// registering its path again does not read the file over. What the
// registry holds, retirements and all, is Entries.
func (r *Registry) Files(projectIDHash string) ([]File, error) {
	entries, err := r.Entries(projectIDHash)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(entries, func(f File) bool { return f.Retired != "" }), nil
}

// Entries lists every entry projectIDHash's registry holds, ordered
// by path, the retired ones included. It is what a caller holding a
// search of the project's tree against the registry asks for: a
// retired path is registered, so calling it unregistered would ask
// the user to register what is registered already. What is still
// read is Files.
func (r *Registry) Entries(projectIDHash string) ([]File, error) {
	reg, err := r.read(projectIDHash)
	if err != nil {
		return nil, err
	}
	entries := append([]File{}, reg.Files...)
	slices.SortFunc(entries, func(a, b File) int { return cmp.Compare(a.Path, b.Path) })
	return entries, nil
}

// Update writes what reading of to.Path reached — the fields a reader
// owns, as advancedTo names them — onto its entry, but only while the
// entry's cursor still stands where from says. An entry whose Offset
// or NextSegment moved since from was read is left as it is, and
// Update fails: to was made from a cursor that is no longer the
// entry's, and writing it would put the cursor past lines that no
// record of the other reader holds.
func (r *Registry) Update(projectIDHash string, from, to File) error {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return err
	}
	if _, err := os.Stat(r.dir); os.IsNotExist(err) {
		return ErrNotRegistered
	}
	return fsatomic.Update(r.path(projectIDHash), 0o600, func(old []byte) ([]byte, error) {
		reg, err := parse(old)
		if err != nil {
			return nil, err
		}
		i, ok := find(reg.Files, to.Path)
		if !ok {
			return nil, ErrNotRegistered
		}
		if cur := reg.Files[i]; cur.Offset != from.Offset || cur.NextSegment != from.NextSegment {
			return nil, errCursorMoved
		}
		reg.Files[i] = reg.Files[i].advancedTo(to)
		return encode(reg)
	})
}

// entry is the registered entry for path as the registry holds it now,
// retired or not. ok is false for a path that is not registered.
func (r *Registry) entry(projectIDHash, path string) (f File, ok bool, err error) {
	reg, err := r.read(projectIDHash)
	if err != nil {
		return File{}, false, err
	}
	i, ok := find(reg.Files, path)
	if !ok {
		return File{}, false, nil
	}
	return reg.Files[i], true, nil
}

// Warm records that a session hook named path at at, from the process
// pid, so the session reads as hot from now on. A pid of 0 keeps the
// process the entries already carry: a hook that cannot tell which
// process runs the session must not erase one that could. A path
// that is not registered is refused, as an update to it is.
func (r *Registry) Warm(projectIDHash, path string, pid int, at time.Time) error {
	return r.mark(projectIDHash, path, func(f *File) {
		f.LastEvent = at.UTC().Format(readAtLayout)
		if pid > 0 {
			f.PID = pid
		}
	})
}

// Cool records that the session writing path is over: the session
// reads as cold until a hook names it again. A path that is not
// registered is refused, as an update to it is.
func (r *Registry) Cool(projectIDHash, path string) error {
	return r.mark(projectIDHash, path, func(f *File) {
		f.LastEvent = ""
		f.PID = 0
	})
}

// mark changes the whole session path belongs to — its main file and
// the agent files registered beside it — in place, leaving every
// cursor as the reader left it. It is how the hot state is written,
// and there are two rules in it. The cursor and the heat have
// different writers, and neither may take the other's fields back in
// time. The heat belongs to the session and not to one of its files,
// so one mark covers the group in one write: a hook names the main
// file only, and an agent file marked on its own would stay cold for
// good.
func (r *Registry) mark(projectIDHash, path string, change func(*File)) error {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return err
	}
	if _, err := os.Stat(r.dir); os.IsNotExist(err) {
		return ErrNotRegistered
	}
	return fsatomic.Update(r.path(projectIDHash), 0o600, func(old []byte) ([]byte, error) {
		reg, err := parse(old)
		if err != nil {
			return nil, err
		}
		if _, ok := find(reg.Files, path); !ok {
			return nil, ErrNotRegistered
		}
		session := SessionOf(path)
		for i := range reg.Files {
			if SessionOf(reg.Files[i].Path) == session {
				change(&reg.Files[i])
			}
		}
		return encode(reg)
	})
}

// Tell records that the session path belongs to has been told the
// device stopped recording, and reports whether this call is the one
// that recorded it. A session already told is answered false, so the
// caller says nothing a second time. The whole session group is
// marked, because a session is told once however many of its files
// exist. A path that is not registered is refused, as an update to it
// is.
func (r *Registry) Tell(projectIDHash, path string) (first bool, err error) {
	err = r.mark(projectIDHash, path, func(f *File) {
		// Only the file the hook named decides. An agent file
		// registered after the session was told would otherwise
		// arrive untold and earn the session a second notice.
		if f.Path == path && !f.Told {
			first = true
		}
		f.Told = true
	})
	return first && err == nil, err
}

// Unregister removes projectIDHash's registry, cursors included. A
// project that has no registry is already unregistered.
func (r *Registry) Unregister(projectIDHash string) error {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return err
	}
	err := os.Remove(r.path(projectIDHash))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Remove drops one file's entry from projectIDHash's registry, leaving
// the project's other entries in place. A path that is not registered,
// or a project with no registry, is already in the wanted state. It is
// how a caller drops a file that is gone for good: nothing is left to
// read of it, and no search of the project finds it again.
func (r *Registry) Remove(projectIDHash, path string) error {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return err
	}
	if _, err := os.Stat(r.dir); os.IsNotExist(err) {
		return nil
	}
	return fsatomic.Update(r.path(projectIDHash), 0o600, func(old []byte) ([]byte, error) {
		reg, err := parse(old)
		if err != nil {
			return nil, err
		}
		i, ok := find(reg.Files, path)
		if !ok {
			return encode(reg)
		}
		reg.Files = append(reg.Files[:i], reg.Files[i+1:]...)
		return encode(reg)
	})
}

// Projects lists the project id hashes that have a registry, sorted.
func (r *Registry) Projects() ([]string, error) {
	entries, err := os.ReadDir(r.dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	projects := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if hash, ok := strings.CutSuffix(e.Name(), registryExt); ok {
			projects = append(projects, hash)
		}
	}
	sort.Strings(projects)
	return projects, nil
}

func (r *Registry) path(projectIDHash string) string {
	return filepath.Join(r.dir, projectIDHash+registryExt)
}

func (r *Registry) read(projectIDHash string) (registry, error) {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return registry{}, err
	}
	data, err := fsatomic.ReadFile(r.path(projectIDHash))
	if os.IsNotExist(err) {
		return registry{Version: version}, nil
	}
	if err != nil {
		return registry{}, err
	}
	return parse(data)
}

// checkProjectIDHash refuses a hash that could name anything but one
// registry file directly under the directory.
func checkProjectIDHash(hash string) error {
	if hash == "" || strings.HasPrefix(hash, ".") || strings.ContainsAny(hash, `/\`) {
		return fmt.Errorf("follow: invalid project id hash %q", hash)
	}
	return nil
}

func parse(data []byte) (registry, error) {
	if data == nil {
		return registry{Version: version}, nil
	}
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return registry{}, fmt.Errorf("follow: registry: %w", err)
	}
	if reg.Version != version {
		return registry{}, fmt.Errorf("follow: unsupported registry version %d", reg.Version)
	}
	// relocated is how outside is written; see File.Outside.
	for i := range reg.Files {
		if reg.Files[i].Retired == relocated {
			reg.Files[i].Retired, reg.Files[i].Outside = "", true
		}
	}
	return reg, nil
}

func encode(reg registry) ([]byte, error) {
	reg.Files = slices.Clone(reg.Files)
	if reg.Files == nil {
		reg.Files = []File{}
	}
	for i := range reg.Files {
		if reg.Files[i].MessageIDs == nil {
			reg.Files[i].MessageIDs = []string{}
		}
		if reg.Files[i].Outside {
			reg.Files[i].Retired = relocated
		}
	}
	return json.Marshal(reg)
}

func find(files []File, path string) (int, bool) {
	for i, f := range files {
		if f.Path == path {
			return i, true
		}
	}
	return 0, false
}
