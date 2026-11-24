package follow

import (
	"cmp"
	"encoding/hex"
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

// The layout before this one kept a whole project in its project file,
// {"version":1,"files":[...]}, one element per entry, with the heat and
// the told mark of a session on each entry of it, and "retired" where
// an entry was to be hidden from some build. A reader of one file held
// <dir>/<project-id-hash>.<path-digest>.lock, and a build that kept
// segment bounds left the bound of a read in flight beside that lock,
// as <dir>/<project-id-hash>.<path-digest>.bound. Migrate is the one
// reader of that layout: nothing else in this package knows it.
//
// A migration runs in six steps, and the project file says how far it
// got. While it is version 1, nothing was done that the next try does
// not do again. Once it is version 2 and the project directory holds
// migratingName, the project is halfway: the entries are still to be
// made from the copy of the version 1 file kept there, and making them
// again changes nothing, so any process may finish it. Without that
// copy, the migration is complete.

const (
	earlierVersion = 1
	migratingName  = "migrating.v1"
	boundExt       = ".bound"
	// earlierLockExt is what the lock of an earlier entry adds to the
	// name it locks.
	earlierLockExt = ".lock"
)

// earlierLockStale is how old a reader of the earlier layout takes the
// lock of an entry to be before it takes the entry over. It is a fact
// of builds already released, so it stays what they do whatever
// bound this build's readers keep.
const earlierLockStale = 2 * time.Minute

// The values of "retired" an earlier build wrote. relocated is the
// form of outside; nestedAgentFile hid an agent file of a workflow run
// from builds that do not know that place, and every build that knows
// this layout knows it.
const (
	relocated       = "relocated"
	nestedAgentFile = "nested-agent-file"
)

// migrationLockStale bounds the hold of the lock that keeps a second
// process from migrating a project one is migrating. It is longer than
// a migration takes: waiting out the readers of an earlier build,
// each of which holds an entry for at most earlierLockStale, and writing
// every entry once and flushing it. A process that finds the lock older
// than that takes it to be one a migration left when it died, and
// takes the migration over.
const migrationLockStale = 5 * time.Minute

// migrationLockWait is how long a process waits for the migration lock
// before it leaves the project to the holder. It is short against any
// read, and long enough to take over a lock left stale, which takes a
// second try after the stale one is removed.
const migrationLockWait = 50 * time.Millisecond

// ErrMigrationPending reports a project whose registry is in the
// layout before this one, or was left halfway to this one. No entry
// of such a project is read or written until Migrate has brought it to
// this layout; a path registered meanwhile is kept as a pending
// registration, which the migration makes an entry of.
var ErrMigrationPending = errors.New("follow: the registry is not migrated to this build's layout yet")

// errMigrationBusy reports a migration another process holds the lock
// of.
var errMigrationBusy = fmt.Errorf("%w (another process is migrating it)", ErrMigrationPending)

// afterMigrationStep runs once each step of a migration is done. It is
// a variable only so a test can stop a migration between two steps, as
// a process killed there would.
var afterMigrationStep = func(int) {}

// beforeCarry runs as step 5 is about to make the entry of path. It is
// a variable only so a test can change the project in between, as
// another process may.
var beforeCarry = func(string) {}

// earlierEntry is one element of "files" in the layout before this one.
type earlierEntry struct {
	File
	Retired   string `json:"retired,omitempty"`
	LastEvent string `json:"last_event,omitempty"`
	PID       int    `json:"pid,omitzero"`
	Told      bool   `json:"told,omitzero"`
}

// earlierRegistry is a project file of the layout before this one.
type earlierRegistry struct {
	Version         int            `json:"version"`
	Files           []earlierEntry `json:"files"`
	Gaps            *Gaps          `json:"gaps,omitempty"`
	Signals         *drift.Signals `json:"signals,omitempty"`
	ReasoningToldOn string         `json:"reasoning_told_on,omitempty"`
}

// errNotEarlier reports a project file that is not, or is no longer,
// of the layout before this one.
var errNotEarlier = errors.New("follow: the registry is not version 1")

func parseEarlier(data []byte) (earlierRegistry, error) {
	var reg earlierRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		return earlierRegistry{}, fmt.Errorf("follow: registry: %w", err)
	}
	if reg.Version != earlierVersion {
		return earlierRegistry{}, errNotEarlier
	}
	return reg, nil
}

// Migrate brings projectIDHash's registry from the layout before this
// one to this one, or finishes a migration another process left
// halfway, and does nothing to a project already in this layout or
// with no registry. It is how every reader but a session hook gets a
// project it can read: a hook never migrates and never waits for one.
// A path a hook registered while the project waited is made an entry,
// unless the earlier layout kept one for it.
// An entry whose session is cold and whose file is gone is not
// carried over: nothing would read it again.
//
// One process migrates a project at a time. A project another process
// is migrating is refused with ErrMigrationPending, for the caller to
// leave until its next run; a process that died while migrating holds
// it until its lock is older than migrationLockStale. A project file of
// the earlier layout that does not parse is not migrated: the error is
// the one reading it would give, and the project stays as it is.
func (r *Registry) Migrate(projectIDHash string) error {
	at, err := r.layoutOf(projectIDHash)
	if err != nil || at == settled {
		return err
	}
	if at == earlier {
		// The copy of the earlier file is staged at step 3 only, but a
		// file that does not parse must be refused before the project
		// directory is touched.
		if _, err := r.earlierPaths(projectIDHash); err != nil {
			return err
		}
	}
	if err := userdirs.EnsureOwnerDir(r.projectDir(projectIDHash)); err != nil {
		return err
	}
	unlock, err := fsatomic.Lock(r.migratingPath(projectIDHash), migrationLockStale, migrationLockWait)
	if err != nil {
		return errMigrationBusy
	}
	defer unlock()
	if err := r.flip(projectIDHash); err != nil {
		return err
	}
	return r.finish(projectIDHash)
}

// layout is how far a project is on the way to this build's layout.
type layout int

const (
	// settled is a project in this layout, or one with no registry.
	settled layout = iota
	// earlier is a project in the layout before this one.
	earlier
	// halfway is a project whose project file is in this layout and
	// whose entries are still to be made from the copy kept.
	halfway
)

func (r *Registry) layoutOf(projectIDHash string) (layout, error) {
	if err := checkProjectIDHash(projectIDHash); err != nil {
		return settled, err
	}
	data, err := fsatomic.ReadFile(r.projectPath(projectIDHash))
	if os.IsNotExist(err) {
		return settled, nil
	}
	if err != nil {
		return settled, err
	}
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return settled, fmt.Errorf("follow: registry: %w", err)
	}
	if head.Version == earlierVersion {
		return earlier, nil
	}
	if _, err := parseProject(data); err != nil {
		return settled, err
	}
	return r.halfwayOrSettled(projectIDHash)
}

// halfwayOrSettled tells a project in this layout whose migration is
// not finished from one that is settled, by the copy a migration
// keeps.
func (r *Registry) halfwayOrSettled(projectIDHash string) (layout, error) {
	_, err := os.Lstat(r.migratingPath(projectIDHash))
	switch {
	case err == nil:
		return halfway, nil
	case errors.Is(err, fs.ErrNotExist):
		return settled, nil
	default:
		return settled, err
	}
}

func (r *Registry) migratingPath(projectIDHash string) string {
	return filepath.Join(r.projectDir(projectIDHash), migratingName)
}

// earlierPaths lists the paths the earlier project file holds now,
// read without its lock. A project file that is no longer of the
// earlier layout, or is gone, holds none.
func (r *Registry) earlierPaths(projectIDHash string) ([]string, error) {
	data, err := fsatomic.ReadFile(r.projectPath(projectIDHash))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	reg, err := parseEarlier(data)
	if errors.Is(err, errNotEarlier) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(reg.Files))
	for _, e := range reg.Files {
		paths = append(paths, e.Path)
	}
	return paths, nil
}

// errMoreEntries reports entries registered since their locks were
// taken.
var errMoreEntries = errors.New("follow: entries were registered since their locks were taken")

// errLocksAged reports entry locks held so long that an earlier
// build's reader may take one over before the project file is
// rewritten.
var errLocksAged = fmt.Errorf("%w (its entry locks grew old before the project file could be rewritten)", ErrMigrationPending)

// earlierLocksStand reports whether every lock in held is still this
// process's to rely on until the project file is rewritten: each is
// there, and none is older than half of earlierLockStale. Locks are
// not renewed, so a process that slept, or waited long for the last of
// them, holds the first ones only in name.
func (r *Registry) earlierLocksStand(projectIDHash string, held map[string]func()) bool {
	for path := range held {
		info, err := os.Lstat(r.earlierLockTarget(projectIDHash, path) + earlierLockExt)
		if err != nil || time.Since(info.ModTime()) > earlierLockStale/2 {
			return false
		}
	}
	return true
}

// flip is steps 1 to 4. It takes the lock of every entry an earlier
// build's reader takes, so no such reader is between storing a segment
// and writing its cursor; then, under the project lock, it keeps a
// copy of the earlier file in a project directory cleared of anything
// left there before, and rewrites the project file in this layout.
// From then on an earlier build reads nothing of the project. A project
// whose project file is not of the earlier layout is left to finish.
// Locks that grew old before the project lock was had are let go and
// taken again, once, so the project file is only ever rewritten under
// locks no earlier reader takes over.
//
// The entry locks come before the project lock: an earlier build's
// reader holds its entry's lock while it waits for the project lock to
// write its cursor, so the other order waits on that reader while it
// waits on us.
func (r *Registry) flip(projectIDHash string) error {
	held := map[string]func(){}
	defer func() {
		for _, unlock := range held {
			unlock()
		}
	}()
	// aged is set once the locks have grown old and been taken again. A
	// second time is left to the next process that migrates.
	aged := false
	for {
		paths, err := r.earlierPaths(projectIDHash)
		if err != nil || len(paths) == 0 && !r.isEarlier(projectIDHash) {
			return err
		}
		if err := r.lockEarlierEntries(projectIDHash, paths, held); err != nil {
			return err
		}
		afterMigrationStep(1)
		err = fsatomic.Update(r.projectPath(projectIDHash), 0o600, func(old []byte) ([]byte, error) {
			if old == nil {
				return nil, errNotEarlier
			}
			reg, err := parseEarlier(old)
			if err != nil {
				return nil, err
			}
			for _, e := range reg.Files {
				if _, ok := held[e.Path]; !ok {
					return nil, errMoreEntries
				}
			}
			if !r.earlierLocksStand(projectIDHash, held) {
				return nil, errLocksAged
			}
			afterMigrationStep(2)
			if err := r.stage(projectIDHash, old); err != nil {
				return nil, err
			}
			afterMigrationStep(3)
			return encode(projectFile{
				Version:         version,
				NeedsBuild:      needsBuild,
				Gaps:            reg.Gaps,
				ReasoningToldOn: reg.ReasoningToldOn,
				Signals:         reg.Signals,
			})
		})
		switch {
		case errors.Is(err, errMoreEntries):
			continue
		case errors.Is(err, errLocksAged) && !aged:
			aged = true
			for path, unlock := range held {
				unlock()
				delete(held, path)
			}
			continue
		case errors.Is(err, errNotEarlier):
			return nil
		case err != nil:
			return err
		}
		for path, unlock := range held {
			unlock()
			delete(held, path)
		}
		afterMigrationStep(4)
		return nil
	}
}

// isEarlier reports whether the project file is of the earlier layout
// now: an earlier file that holds no entry still has a migration to
// make.
func (r *Registry) isEarlier(projectIDHash string) bool {
	at, err := r.layoutOf(projectIDHash)
	return err == nil && at == earlier
}

// earlierLockTarget is the name an earlier build's reader of path
// locks the entry under.
func (r *Registry) earlierLockTarget(projectIDHash, path string) string {
	return filepath.Join(r.dir, projectIDHash+"."+digest(path))
}

// lockEarlierEntries takes, in path order, the earlier lock of every
// path held does not hold yet, waiting for each as long as an earlier
// reader may hold it, and taking it over from a reader that held it
// longer, as those readers do among themselves.
func (r *Registry) lockEarlierEntries(projectIDHash string, paths []string, held map[string]func()) error {
	slices.Sort(paths)
	for _, path := range paths {
		if _, ok := held[path]; ok {
			continue
		}
		unlock, err := fsatomic.Lock(r.earlierLockTarget(projectIDHash, path), earlierLockStale, earlierLockStale)
		if err != nil {
			return err
		}
		held[path] = unlock
	}
	return nil
}

// stage is step 3: it empties the project directory of everything but
// the migration's own lock and the pending registrations, those placed
// and those being placed, and keeps earlier there, flushed, as the copy
// the entries are made from.
func (r *Registry) stage(projectIDHash string, earlier []byte) error {
	dir := r.projectDir(projectIDHash)
	names, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		if strings.HasPrefix(name.Name(), migratingName+".lock") || strings.Contains(name.Name(), pendingExt) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name.Name())); err != nil {
			return err
		}
	}
	return fsatomic.WriteFile(r.migratingPath(projectIDHash), earlier, 0o600)
}

// finish is steps 5 and 6 of a project that is halfway, and does
// nothing to any other. A project whose copy another process removed
// meanwhile is left as that process finished it. The pending
// registrations are made entries once the copy's entries stand, so an
// entry the earlier layout kept wins over them, and again once the
// copy is removed, for those a hook placed in between.
func (r *Registry) finish(projectIDHash string) error {
	at, err := r.layoutOf(projectIDHash)
	if err != nil || at != halfway {
		return err
	}
	data, err := fsatomic.ReadFile(r.migratingPath(projectIDHash))
	if err != nil {
		return err
	}
	reg, err := parseEarlier(data)
	if err != nil {
		return err
	}
	switch err := r.carryOver(projectIDHash, reg); {
	case errors.Is(err, errCopyGone):
		return nil
	case err != nil:
		return err
	}
	if err := r.takePending(projectIDHash); err != nil {
		return err
	}
	afterMigrationStep(5)
	if err := r.settle(projectIDHash); err != nil {
		return err
	}
	afterMigrationStep(6)
	return r.takePending(projectIDHash)
}

// carryOver is step 5: it makes an entry for every element of reg but
// the cold ones whose file is gone, the heat of every session a hook
// named, and the told mark of every session that was told, and
// replaces none of them that stands already, but for an entry that
// does not parse. A session none of whose entries is made gets neither
// heat nor a told mark. Nothing reads the project while it is halfway,
// so what stands was made by an earlier try of this step. The files are
// not flushed one by one; step 6 flushes them all.
//
// Before each file it makes, it looks for the copy, and stops with
// errCopyGone when the copy is gone: another process finished the
// migration, and readers may have removed entries since, or the
// project was unregistered. Making any file from the copy then would
// put back what was removed.
func (r *Registry) carryOver(projectIDHash string, reg earlierRegistry) error {
	latest := map[string]earlierEntry{}
	told := map[string]bool{}
	for _, old := range reg.Files {
		session := SessionOf(old.Path)
		if old.later(latest[session]) {
			latest[session] = old
		}
		if old.Told && old.MainSession() {
			told[session] = true
		}
	}
	now := time.Now()
	carried := map[string]string{}
	for _, old := range reg.Files {
		beforeCarry(old.Path)
		if err := r.copyStands(projectIDHash); err != nil {
			return err
		}
		session := SessionOf(old.Path)
		e := old.carried()
		warm := e.File
		warm.LastEvent, warm.PID = latest[session].LastEvent, latest[session].PID
		if e.readable() && warm.prunable(now, ProcessAlive) {
			continue
		}
		if b, ok := r.earlierBound(projectIDHash, old.Path); ok {
			e.Bound = &b
		}
		data, err := encode(e)
		if err != nil {
			return err
		}
		if err := carryEntry(r.entryPath(projectIDHash, old.Path), data); err != nil {
			return err
		}
		carried[session] = old.Path
	}
	for session, path := range carried {
		if err := r.copyStands(projectIDHash); err != nil {
			return err
		}
		if told[session] {
			if _, err := r.tell(projectIDHash, path); err != nil {
				return err
			}
		}
		h, ok := latest[session]
		if !ok {
			continue
		}
		data, err := encode(heat{LastEvent: h.LastEvent, PID: h.PID})
		if err != nil {
			return err
		}
		if err := r.copyStands(projectIDHash); err != nil {
			return err
		}
		if err := placeNew(r.sessionPath(projectIDHash, path, heatExt), data, false); err != nil {
			return err
		}
	}
	return nil
}

// errCopyGone reports a migration whose copy was removed while it made
// entries from it.
var errCopyGone = errors.New("follow: the copy of the earlier registry is gone")

// copyStands refuses with errCopyGone once projectIDHash's copy is
// gone.
func (r *Registry) copyStands(projectIDHash string) error {
	at, err := r.halfwayOrSettled(projectIDHash)
	if err != nil {
		return err
	}
	if at != halfway {
		return errCopyGone
	}
	return nil
}

// carryEntry puts data at target, the entry file of one element of the
// copy, unless an entry that parses stands there. An earlier try of
// step 5 that lost power before step 6 flushed it can leave an entry
// empty or cut short; such an entry holds no state of its own while
// the copy stands, so it is replaced whole from the copy. This is the
// one place an entry that does not parse is rewritten; everywhere else
// it is left as it is.
func carryEntry(target string, data []byte) error {
	standing, err := fsatomic.ReadFile(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return placeNew(target, data, false)
	case err != nil:
		return err
	}
	var e entryFile
	if json.Unmarshal(standing, &e) == nil {
		return nil
	}
	return fsatomic.WriteFile(target, data, 0o600)
}

// carried is e as an entry of this layout. Outside had the form of a
// retirement; the retirement of an agent file of a workflow run hides
// it from no build that reads this layout; and a retirement no build
// wrote is kept as what the entry needs, so no build reads it.
func (e earlierEntry) carried() entryFile {
	carried := entryFile{File: e.File}
	switch e.Retired {
	case "", nestedAgentFile:
	case relocated:
		carried.Outside = true
	default:
		carried.Needs = []string{e.Retired}
	}
	return carried
}

// later reports whether a hook named e's session later than than, and
// e can say when. An entry no hook named carries no heat.
func (e earlierEntry) later(than earlierEntry) bool {
	at, err := time.Parse(readAtLayout, e.LastEvent)
	if err != nil {
		return false
	}
	was, err := time.Parse(readAtLayout, than.LastEvent)
	return err != nil || at.After(was)
}

// pendingExt names a pending registration: a path a hook registered
// while the project waited for its migration.
const pendingExt = ".pending"

// pendingRegistration is what a pending registration holds: the path,
// and where the session ran. It holds nothing about reading.
type pendingRegistration struct {
	Path    string `json:"path"`
	Subpath string `json:"subpath,omitempty"`
}

func (r *Registry) pendingPath(projectIDHash, path string) string {
	return filepath.Join(r.projectDir(projectIDHash), digest(path)+pendingExt)
}

// registerPending keeps each of paths as a pending registration of
// projectIDHash, whose registry waits for its migration, and leaves one
// that stands as it is. Each is placed whole, as an entry is. pending
// is the refusal the registration met, and is what it gets while the
// project still waits.
//
// The layout is judged again once they are placed, and a project
// settled by then has them made entries here: a migration makes
// entries of them after it removes its copy, and this judges after it
// placed them, so whichever comes last finds what the other did.
func (r *Registry) registerPending(projectIDHash string, paths []string, subpath string, pending error) error {
	if err := userdirs.EnsureOwnerDir(r.projectDir(projectIDHash)); err != nil {
		return err
	}
	for _, path := range paths {
		data, err := encode(pendingRegistration{Path: path, Subpath: subpath})
		if err != nil {
			return err
		}
		if err := placeNew(r.pendingPath(projectIDHash, path), data, true); err != nil {
			return err
		}
	}
	if _, ok, err := r.readProject(projectIDHash); err != nil || !ok {
		return cmp.Or(err, pending)
	}
	return r.takePending(projectIDHash)
}

// takePending makes an entry of every pending registration of
// projectIDHash, and removes it. An entry that stands for its path is
// left as it is, whatever it holds: what the earlier layout kept about
// the file wins over a registration that knows nothing about reading
// it. A pending registration that cannot be read is left where it is.
func (r *Registry) takePending(projectIDHash string) error {
	dir := r.projectDir(projectIDHash)
	names, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, name := range names {
		if name.IsDir() || filepath.Ext(name.Name()) != pendingExt {
			continue
		}
		file := filepath.Join(dir, name.Name())
		p, ok := readPending(file)
		if !ok {
			continue
		}
		if err := r.create(projectIDHash, entryFile{Path: p.Path, Subpath: p.Subpath}); err != nil {
			return err
		}
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// readPending is the pending registration in file. ok is false for one
// that cannot be read, does not parse, or names no absolute path.
func readPending(file string) (p pendingRegistration, ok bool) {
	data, err := fsatomic.ReadFile(file)
	if err != nil || json.Unmarshal(data, &p) != nil || !filepath.IsAbs(p.Path) {
		return pendingRegistration{}, false
	}
	return p, true
}

// earlierBound is the bound a build that kept bounds left beside the
// earlier lock of path's entry. A bound that cannot be read is no
// bound.
func (r *Registry) earlierBound(projectIDHash, path string) (b segmentBound, ok bool) {
	data, err := fsatomic.ReadFile(r.earlierLockTarget(projectIDHash, path) + boundExt)
	if err != nil || json.Unmarshal(data, &b) != nil {
		return segmentBound{}, false
	}
	return b, true
}

// settle is step 6: every entry of the project directory is flushed,
// whichever try of step 5 made it, and the directory after them; then
// what the earlier layout kept beside the project file is removed; and
// last the copy, which completes the migration. The copy goes last
// because a project without it is never settled again.
func (r *Registry) settle(projectIDHash string) error {
	dir := r.projectDir(projectIDHash)
	names, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name.IsDir() || filepath.Ext(name.Name()) != jsonExt {
			continue
		}
		if err := flushFile(filepath.Join(dir, name.Name())); err != nil {
			return err
		}
	}
	flushDir(dir)
	r.dropEarlierFiles(projectIDHash)
	if err := os.Remove(r.migratingPath(projectIDHash)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	flushDir(dir)
	return nil
}

// dropEarlierFiles removes the entry locks and the bounds the earlier
// layout kept beside projectIDHash's project file. One left behind is
// harmless: nothing of this layout reads it.
func (r *Registry) dropEarlierFiles(projectIDHash string) {
	names, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	for _, name := range names {
		rest, ok := strings.CutPrefix(name.Name(), projectIDHash+".")
		if !ok || name.IsDir() {
			continue
		}
		pathDigest, kind, _ := strings.Cut(rest, ".")
		if _, err := hex.DecodeString(pathDigest); err != nil || len(pathDigest) != 32 {
			continue
		}
		if kind == "lock" || strings.HasPrefix(kind, strings.TrimPrefix(boundExt, ".")) {
			_ = os.Remove(filepath.Join(r.dir, name.Name()))
		}
	}
}

// flushFile makes what file holds durable.
func flushFile(file string) error {
	f, err := os.OpenFile(file, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return cmp.Or(f.Sync(), f.Close())
}

// flushDir makes the names dir holds durable, as far as the platform
// lets a directory be flushed.
func flushDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
