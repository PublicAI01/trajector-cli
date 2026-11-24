// Package follow keeps the registry of files trajector reads for an
// enabled project, and how far each has been read. The layout is a
// documented product contract:
//
//	<dir>/<project-id-hash>.json        the project file
//	<dir>/<project-id-hash>.json.lock   held while the project file is rewritten
//	<dir>/<project-id-hash>/            the project directory
//	    <path-digest>.json              one entry per file read
//	    <path-digest>.lock              held while that file is read
//	    <session-digest>.heat           when a hook last named the session
//	    <session-digest>.told           the session was told recording stopped
//	    <path-digest>.pending           a registration waiting for a migration
//
// A path digest is the first 16 bytes of the SHA-256 of the file's
// path, in hex, and a session digest is the same of the session's name
// as SessionOf gives it: the names tell no more than the files do.
// Directories are 0700 and files 0600: the paths alone reveal what the
// user works on.
//
// The project file is one JSON object: "version", the layout, which is
// 2; "needs_build", the lowest trajector build that reads the layout;
// and optionally "gaps", what the one-time search for the project's
// earlier files could not cover, so a later reading can say so without
// searching again; "reasoning_told_on", the UTC day, as YYYY-MM-DD, on
// which a session of the project was last told that most of its
// reasoning blocks carry no text; and "signals", what reading noticed
// about entries since removed.
//
// An entry file is one JSON object carrying path, inode, size, offset,
// next_segment, and message_ids, and optionally subpath, read_at,
// before_rewrite, outside, empty_reasoning_blocks,
// reasoning_blocks_with_text, launch_surface, signals, bound, and
// needs. The path is the entry's identity. Inode, size, and offset
// describe the file as it was last observed locally: they steer reading
// and never leave it. Outside is true while the session is in a
// directory consent does not cover. Signals is what the reader noticed
// about the shape of the lines it read from the file, as counts and
// field names only, so a surface can state it without opening a session
// file; it is kept in the form drift gives it and summed by drift's own
// arithmetic. Bound is where the segment a read handed to the store
// ends. It stands until a write of the cursor drops it: between the two
// writes of a read whose segment is stored, and from read to read
// while the segment waits to be stored. An entry
// is created whole or not at all, and a registration never replaces
// one. An entry file that does not parse is passed over, and never read
// from its start again; Unreadable counts such files.
//
// A pending registration is {"path":…,"subpath":…}: a file a hook
// registered while the project's registry waited for its migration. It
// is placed whole and never replaced, as an entry is, and the migration
// makes an entry of it and removes it; an entry the earlier layout kept
// for the same path wins over it.
//
// A heat file is {"last_event":"<RFC 3339>","pid":<n>}, rewritten whole
// when a hook or the resident process names the session, and no more
// often than once a minute by one process. A told mark is an empty file:
// the call that creates it is the one that tells.
//
// A change to the layout is one of three kinds. One that only adds a
// key or a kind of file moves nothing: every writer of an entry or of
// the project file writes back the keys it does not know, and a file in
// the project directory whose extension a build does not know is
// ignored. That holds for the keys of the file itself, not for a key
// added inside signals, gaps, or bound, which a build that does not
// know it drops when it rewrites the member: a key that must outlive
// every writer is added to the file itself. An entry that a build must know more to read correctly
// lists what it needs, by name, under "needs": a build that does not
// know one of the names does not list, read, warm, rewrite, or remove
// the entry, and reads the rest of the project. A change of the whole
// layout raises "version": a build that meets one it does not know
// stops reading the project, and names the build it needs. A project
// in the layout before this one is read by Migrate alone, which brings
// it to this one.
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
	"crypto/sha256"
	"encoding/hex"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// version is the registry layout written by this package. A project
// file carrying any other version is refused rather than interpreted:
// a cursor read under the wrong layout would silently skip or repeat
// content.
const version = 2

// needsBuild is the lowest trajector build that reads the layout this
// package writes. Every project file this build creates names it, so a
// later build that meets the layout and does not know it can say which
// build is needed.
const needsBuild = "0.8.0"

const jsonExt = ".json"

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

// projectFile is what the project file holds.
type projectFile struct {
	Version    int    `json:"version"`
	NeedsBuild string `json:"needs_build,omitempty"`
	Gaps       *Gaps  `json:"gaps,omitempty"`
	// ReasoningToldOn belongs to the project, not to a session: the
	// line is worth one turn of the user's attention a day, however
	// many sessions run.
	ReasoningToldOn string `json:"reasoning_told_on,omitempty"`
	// Signals is what reading noticed in entries since removed: what
	// was noticed about a project's files outlives the entries for the
	// files themselves.
	Signals *drift.Signals `json:"signals,omitempty"`
	// Unknown holds the members this build does not know, written back
	// as they were found.
	Unknown jsontext.Value `json:",embed"`
}

// RegisterAll adds each of paths to projectIDHash's registry with a
// zero cursor, recording where the session ran relative to the project
// root. Registering a path that is already registered changes nothing,
// its subpath included: the first registration of a session's file is
// the one that knows where it ran. When every path is registered
// already, nothing is written. Each path must be absolute: it is the
// entry's identity across processes with different working
// directories. The first registration of a project creates its project
// file. A project whose registry waits for its migration keeps each
// path as a pending registration, which the migration makes an entry
// of, and the registration is refused with ErrMigrationPending: nothing
// here waits for a migration.
func (r *Registry) RegisterAll(projectIDHash string, paths []string, subpath string) error {
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("follow: path %q is not absolute", path)
		}
	}
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	switch err := r.ensureProject(projectIDHash); {
	case errors.Is(err, ErrMigrationPending):
		return r.registerPending(projectIDHash, paths, subpath, err)
	case err != nil:
		return err
	}
	if err := userdirs.EnsureOwnerDir(r.projectDir(projectIDHash)); err != nil {
		return err
	}
	for _, path := range paths {
		if err := r.create(projectIDHash, entryFile{Path: path, Subpath: subpath}); err != nil {
			return err
		}
	}
	return nil
}

// ensureProject creates projectIDHash's project file when it has none,
// and refuses a project file of another layout.
func (r *Registry) ensureProject(projectIDHash string) error {
	if _, ok, err := r.readProject(projectIDHash); err != nil || ok {
		return err
	}
	if err := userdirs.EnsureOwnerDir(r.dir); err != nil {
		return err
	}
	return r.updateProject(projectIDHash, true, func(*projectFile) error { return fsatomic.ErrUnchanged })
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
	return r.updateProject(projectIDHash, true, func(p *projectFile) error {
		p.Gaps = nil
		if gaps.Any() {
			p.Gaps = &gaps
		}
		return nil
	})
}

// Gaps reports what the latest search for projectIDHash's earlier
// files left uncovered. A project with no registry, or one whose
// search covered everything, has no gaps.
func (r *Registry) Gaps(projectIDHash string) (Gaps, error) {
	p, _, err := r.readProject(projectIDHash)
	if err != nil || p.Gaps == nil {
		return Gaps{}, err
	}
	return *p.Gaps, nil
}

// plus is what sum holds with more added, and nil when that is nothing.
// What a signal holds is drift's to name; the registry only keeps the
// sum.
func plus(sum *drift.Signals, more drift.Signals) *drift.Signals {
	var total drift.Signals
	if sum != nil {
		total = *sum
	}
	total = total.Add(more)
	if !total.Any() {
		return nil
	}
	return &total
}

// Contents is one project's registry as a caller that states or acts
// on the project as a whole reads it. Every field comes from one
// listing of the project directory, so no two of them describe
// different moments.
type Contents struct {
	// Files is every file the project reads, ordered by path, each with
	// the heat of its session. An entry that needs what this build does
	// not know is not among them, and neither is an entry file that does
	// not parse.
	Files []File
	// Paths is the path of every entry the registry holds, ordered, the
	// entries this build does not read included. It is what a search of
	// the project's tree is held against: an entry a later build needs is
	// registered, and calling its path unregistered would ask the user to
	// register what is registered already. An entry file that does not
	// parse names no path.
	Paths []string
	// Signals is everything the reader noticed about the project's files
	// so far: what each file in Files noticed, and what the entries
	// removed since noticed.
	Signals drift.Signals
	// Gaps is what the latest search for the project's earlier files left
	// uncovered.
	Gaps Gaps
}

// Contents reads projectIDHash's registry once. A project with no
// registry has empty contents.
func (r *Registry) Contents(projectIDHash string) (Contents, error) {
	p, entries, _, err := r.listAll(projectIDHash)
	if err != nil {
		return Contents{}, err
	}
	c := Contents{Files: make([]File, 0, len(entries)), Paths: make([]string, 0, len(entries))}
	if p.Gaps != nil {
		c.Gaps = *p.Gaps
	}
	sum := p.Signals
	heats := map[string]heat{}
	for _, e := range entries {
		c.Paths = append(c.Paths, e.Path)
		if !e.readable() {
			continue
		}
		c.Files = append(c.Files, r.warmed(projectIDHash, e.File, heats))
		if e.Signals != nil {
			sum = plus(sum, *e.Signals)
		}
	}
	if sum != nil {
		c.Signals = *sum
	}
	return c, nil
}

// Signals is what Contents states about projectIDHash's signals.
func (r *Registry) Signals(projectIDHash string) (drift.Signals, error) {
	c, err := r.Contents(projectIDHash)
	return c.Signals, err
}

// Files is what Contents states about the files projectIDHash reads.
func (r *Registry) Files(projectIDHash string) ([]File, error) {
	c, err := r.Contents(projectIDHash)
	if err != nil {
		return nil, err
	}
	return c.Files, nil
}

// Unreadable counts the entry files of projectIDHash's registry that
// could not be read or do not parse. Such a file is passed over by
// every other reading, and the file it is the entry of is not read, so
// a surface states the count; nothing it holds is shown.
func (r *Registry) Unreadable(projectIDHash string) (int, error) {
	_, _, unreadable, err := r.listAll(projectIDHash)
	return unreadable, err
}

// File is the entry projectIDHash reads for path. ok is false for a
// path that is not registered, and for one Files would not list.
func (r *Registry) File(projectIDHash, path string) (f File, ok bool, err error) {
	if _, _, err := r.readProject(projectIDHash); err != nil {
		return File{}, false, err
	}
	e, ok, err := r.readEntry(projectIDHash, path)
	if err != nil || !ok || !e.readable() {
		return File{}, false, err
	}
	return r.warmed(projectIDHash, e.File, nil), true, nil
}

// SessionFiles lists the files projectIDHash reads that belong to the
// session path belongs to, ordered by path: the main file and the
// agent files kept under it. A session with no registered file has
// none.
func (r *Registry) SessionFiles(projectIDHash, path string) ([]File, error) {
	files, err := r.Files(projectIDHash)
	if err != nil {
		return nil, err
	}
	return group(files, path), nil
}

// HotFiles lists the files projectIDHash reads whose session is hot at
// now, ordered by path. alive answers whether a process id names a
// running process.
func (r *Registry) HotFiles(projectIDHash string, now time.Time, alive func(pid int) bool) ([]File, error) {
	files, err := r.Files(projectIDHash)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(files, func(f File) bool { return !f.Hot(now, alive) }), nil
}

// TellReasoning records that a session of projectIDHash was told, on
// the UTC day of at, that most of its reasoning blocks carry no text,
// and reports whether this call is the one that recorded it. A
// project already told on that day is answered false, so the caller
// says nothing a second time that day. A project with no registry is
// refused, as an update to it is.
func (r *Registry) TellReasoning(projectIDHash string, at time.Time) (first bool, err error) {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return false, err
	}
	if _, err := os.Stat(r.dir); os.IsNotExist(err) {
		return false, ErrNotRegistered
	}
	day := at.UTC().Format(time.DateOnly)
	err = r.updateProject(projectIDHash, false, func(p *projectFile) error {
		if p.ReasoningToldOn == day {
			return fsatomic.ErrUnchanged
		}
		p.ReasoningToldOn = day
		first = true
		return nil
	})
	return first && err == nil, err
}

// Unregister removes projectIDHash's registry: its project directory,
// every entry and cursor in it, and its project file, in whichever
// layout it is. A project that has no registry is already
// unregistered.
func (r *Registry) Unregister(projectIDHash string) error {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return err
	}
	r.dropEarlierFiles(projectIDHash)
	if err := os.RemoveAll(r.projectDir(projectIDHash)); err != nil {
		return err
	}
	err := os.Remove(r.projectPath(projectIDHash))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// leave removes the entries of held, whose locks the caller holds,
// from projectIDHash's registry, leaving the project's other entries in
// place, and reports how many it removed. What reading their files
// noticed is kept in the project file, in one write for them all,
// before any of them is removed, and a session whose last entry is
// removed loses its heat and its told mark with it. An entry removed
// already is in the wanted state; an entry that needs what this build
// does not know, or whose file does not parse, is left as it is. It is
// the one way an entry leaves: the reader of a file gone for good drops
// its entry, and a prune the cold ones; nothing is left to read of
// them, and no search of the project finds them again.
func (r *Registry) leave(projectIDHash string, held []*heldEntry) (removed int, err error) {
	var noticed *drift.Signals
	var leaving []*heldEntry
	for _, h := range held {
		e, ok, err := h.read()
		if err != nil || !ok || !e.readable() {
			continue
		}
		leaving = append(leaving, h)
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
	for _, h := range leaving {
		if err := r.dropEntry(projectIDHash, h.path); err != nil {
			return removed, err
		}
		removed++
		sessions[SessionOf(h.path)] = h.path
	}
	if len(sessions) == 0 {
		return removed, nil
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

// dropEntry removes the entry file of path. An entry removed already is
// removed.
func (r *Registry) dropEntry(projectIDHash, path string) error {
	err := os.Remove(r.entryPath(projectIDHash, path))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Projects lists the project id hashes that have a project file,
// sorted.
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
		if hash, ok := strings.CutSuffix(e.Name(), jsonExt); ok {
			projects = append(projects, hash)
		}
	}
	slices.Sort(projects)
	return projects, nil
}

func (r *Registry) projectPath(projectIDHash string) string {
	return filepath.Join(r.dir, projectIDHash+jsonExt)
}

func (r *Registry) projectDir(projectIDHash string) string {
	return filepath.Join(r.dir, projectIDHash)
}

// digest names a file of the project directory after s without
// spelling s.
func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

// readProject is projectIDHash's project file. ok is false for a
// project that has none. A project whose migration is not finished is
// refused with ErrMigrationPending.
func (r *Registry) readProject(projectIDHash string) (p projectFile, ok bool, err error) {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return projectFile{}, false, err
	}
	data, err := fsatomic.ReadFile(r.projectPath(projectIDHash))
	if os.IsNotExist(err) {
		return projectFile{}, false, nil
	}
	if err != nil {
		return projectFile{}, false, err
	}
	if p, err = parseProject(data); err != nil {
		return projectFile{}, false, err
	}
	if at, err := r.halfwayOrSettled(projectIDHash); err != nil || at == halfway {
		return projectFile{}, false, cmp.Or(err, fmt.Errorf("%w (its migration is not finished)", ErrMigrationPending))
	}
	return p, true, nil
}

// updateProject rewrites projectIDHash's project file with change
// applied, under the project lock. A project with no project file gets
// one when create is set, and is refused otherwise. A project directory
// that stands without a project file is what an earlier build's
// disable left; it is emptied before the project file is created, so
// nothing in it is read as an entry of the new registration.
func (r *Registry) updateProject(projectIDHash string, create bool, change func(*projectFile) error) error {
	return fsatomic.Update(r.projectPath(projectIDHash), 0o600, func(old []byte) ([]byte, error) {
		p := projectFile{Version: version, NeedsBuild: needsBuild}
		if old == nil && !create {
			return nil, ErrNotRegistered
		}
		if old == nil {
			if err := os.RemoveAll(r.projectDir(projectIDHash)); err != nil {
				return nil, err
			}
		}
		if old != nil {
			var err error
			if p, err = parseProject(old); err != nil {
				return nil, err
			}
		}
		err := change(&p)
		if err != nil && (old != nil || !errors.Is(err, fsatomic.ErrUnchanged)) {
			return nil, err
		}
		return encode(p)
	})
}

func parseProject(data []byte) (projectFile, error) {
	var p projectFile
	if err := json.Unmarshal(data, &p); err != nil {
		return projectFile{}, fmt.Errorf("follow: registry: %w", err)
	}
	switch {
	case p.Version == version:
		return p, nil
	case p.Version == earlierVersion:
		return projectFile{}, fmt.Errorf("%w (registry version %d)", ErrMigrationPending, p.Version)
	case p.Version > version && p.NeedsBuild != "":
		return projectFile{}, fmt.Errorf("follow: registry version %d needs trajector %s or later", p.Version, p.NeedsBuild)
	default:
		return projectFile{}, fmt.Errorf("follow: unsupported registry version %d", p.Version)
	}
}

// listAll is projectIDHash's project file and every entry of it,
// ordered by path, and how many entry files could not be read. An
// entry file that vanished since the directory was listed is passed
// over.
func (r *Registry) listAll(projectIDHash string) (p projectFile, entries []entryFile, unreadable int, err error) {
	p, _, err = r.readProject(projectIDHash)
	if err != nil {
		return projectFile{}, nil, 0, err
	}
	dir := r.projectDir(projectIDHash)
	names, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return p, nil, 0, nil
	}
	if err != nil {
		return projectFile{}, nil, 0, err
	}
	for _, name := range names {
		if name.IsDir() || filepath.Ext(name.Name()) != jsonExt {
			continue
		}
		e, err := readEntryFile(filepath.Join(dir, name.Name()))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			unreadable++
		default:
			entries = append(entries, e)
		}
	}
	slices.SortFunc(entries, func(a, b entryFile) int { return cmp.Compare(a.Path, b.Path) })
	return p, entries, unreadable, nil
}

// checkProjectIDHash refuses a hash that could name anything but one
// project file and one project directory directly under the directory.
func checkProjectIDHash(hash string) error {
	if hash == "" || strings.HasPrefix(hash, ".") || strings.ContainsAny(hash, `/\`) {
		return fmt.Errorf("follow: invalid project id hash %q", hash)
	}
	return nil
}

// encode is the JSON form of a file of the registry. Keys tagged
// omitempty are left out when they hold a zero, as drift's signals
// state them.
func encode(v any) ([]byte, error) {
	return json.Marshal(v, jsonv1.OmitEmptyWithLegacySemantics(true))
}
