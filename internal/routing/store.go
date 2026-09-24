package routing

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// Store is the CLI-side writer for the routing table. The proxy only
// ever reads the table (through Table); every mutation goes through a
// Store method so the file is always rewritten atomically and stays
// loadable by a concurrently running proxy.
type Store struct {
	path string
}

// OpenStore returns a writer for the table at path.
func OpenStore(path string) *Store { return &Store{path: path} }

// Shape is the form of a project's injection: whether the project's
// traffic goes through the proxy, or the session hooks stand alone and
// only the files a session leaves are read. The grant is where the
// user's choice is recorded, so the type lives here and every surface
// reads the shape from the grant; a settings file states the same
// choice but is a file the user also edits, and what it says is a
// reading to reconcile, never a second answer.
//
// The table keeps storing the choice as the boolean it has always
// stored: the type is what the code passes around, not a new format.
type Shape string

const (
	// WithProxy routes the project's traffic through the proxy: a base
	// URL is injected beside the session hooks.
	WithProxy Shape = "with_proxy"
	// WithoutProxy installs the session hooks alone: the project's
	// traffic goes wherever it went before, and only the files its
	// sessions leave are read.
	WithoutProxy Shape = "without_proxy"
)

// Grant is one project's consent record: which project, where it lives,
// and the token that identifies its traffic. The proxy never sees these
// fields; it only needs a Route.
type Grant struct {
	Token         string
	ProjectIDHash string
	RootPath      string
	Upstream      string
	GrantedAt     string
	// UpstreamMoved carries the last recorded unattended upstream
	// change, zero when the upstream still is what enable granted.
	UpstreamMoved UpstreamMove
	// Revoked reports the per-entry revocation only, never a device-wide
	// pause. A caller that wants to know whether traffic is being
	// recorded must ask the Table.
	Revoked bool
	// Shape is the form enable installed. A grant read back from the
	// table always carries one of the two shapes.
	Shape Shape
	// EarlierSkipped records that the user asked enable to leave the
	// session files that predate the grant alone. It is the answer
	// every surface reads: nothing else on this device states that
	// the files a project already had are not collected.
	EarlierSkipped bool
}

// GrantedAtTime is when the project was enabled. A record that does
// not hold the time in the layout the table writes holds no time at
// all, and says so: a caller that guessed one would date this
// project's session files against it. The layout is read here because
// the record is this package's own.
func (g Grant) GrantedAtTime() (time.Time, bool) {
	at, err := time.Parse(time.RFC3339, g.GrantedAt)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// UpstreamMove is one recorded unattended upstream change: where the
// upstream stood and when it moved.
type UpstreamMove struct {
	From string
	At   string
}

// Happened reports whether a move was recorded.
func (m UpstreamMove) Happened() bool { return m.At != "" }

// Grant installs the record for one project. A re-enabled project
// rotates its token rather than resurrecting an old one, so no previous
// entry for the same root path stays active — but they are retired, not
// deleted, which is the same rule Revoke states and holds for the same
// reason.
//
// Deleting them was a hole in exactly the guarantee Revoke exists for. A
// Claude Code session reads the injected base URL once, when it starts,
// and carries that token in its environment for the rest of its life; a
// `disable` followed by an `enable` under such a session leaves it
// sending a token this table no longer knows. An unknown token resolves
// to nothing, and the data path answers that with the default upstream —
// which for a project chained to a third-party relay means the relay's
// own credential headers go to the official endpoint, the one guess
// enable, disable, and the unattended reconcile each refuse to make.
// Retired entries keep forwarding where they were granted to and record
// nothing, so the residual injection stays harmless until the session
// ends. 2026-09-18.
func (s *Store) Grant(g Grant) error {
	return s.update(func(f *tableFile) {
		for tok, rec := range f.Projects {
			if tok == g.Token || rec.RootPath != g.RootPath || rec.RevokedAt != "" {
				continue
			}
			rec.RevokedAt = g.GrantedAt
			f.Projects[tok] = rec
		}
		f.Projects[g.Token] = projectRecord{
			ProjectIDHash: g.ProjectIDHash,
			RootPath:      g.RootPath,
			Upstream:      g.Upstream,
			GrantedAt:     g.GrantedAt,
			NoProxy:       g.Shape == WithoutProxy,
			NoEarlier:     g.EarlierSkipped,
		}
	})
}

// Revoke marks every entry for rootPath revoked at the given timestamp.
// Entries are kept, not deleted: a revoked route must keep forwarding
// to its recorded upstream so residual injection cannot break a chained
// third-party setup, while recording stays off.
func (s *Store) Revoke(rootPath, at string) error {
	return s.update(func(f *tableFile) {
		for tok, rec := range f.Projects {
			if rec.RootPath == rootPath && rec.RevokedAt == "" {
				rec.RevokedAt = at
				f.Projects[tok] = rec
			}
		}
	})
}

// SetUpstream updates the upstream of the active entry for rootPath,
// used when the user's own base-URL configuration drifts after enable.
// The move is recorded on the entry — where the upstream stood and
// when it changed — because it happens where no user is watching and
// must stay visible afterwards.
func (s *Store) SetUpstream(rootPath, upstream, at string) error {
	return s.update(func(f *tableFile) {
		for tok, rec := range f.Projects {
			if rec.RootPath == rootPath && rec.RevokedAt == "" {
				rec.UpstreamMoved = &upstreamMoveRecord{From: rec.Upstream, At: at}
				rec.Upstream = upstream
				f.Projects[tok] = rec
			}
		}
	})
}

// GrantSnapshot captures one root path's table entries so a failed
// enable can put exactly them back. The value is opaque: only the store
// that minted it can restore it.
type GrantSnapshot struct {
	rootPath string
	entries  map[string]projectRecord
}

// SnapshotGrants captures rootPath's current entries, revoked ones
// included. A root with no entries yields a snapshot that restores to
// absence.
func (s *Store) SnapshotGrants(rootPath string) (GrantSnapshot, error) {
	f, err := readTableFile(s.path)
	if err != nil {
		return GrantSnapshot{}, err
	}
	snap := GrantSnapshot{rootPath: rootPath, entries: map[string]projectRecord{}}
	for tok, rec := range f.Projects {
		if rec.RootPath == rootPath {
			snap.entries[tok] = rec
		}
	}
	return snap, nil
}

// RestoreGrants puts the snapshotted root path back to its captured
// state. Only that root's entries are touched: the restore runs under
// the same serialized update as every other mutation, so a concurrent
// process's grant for another project is never lost to a rollback. A
// table already matching the snapshot is left alone entirely — an
// enable that failed before writing must be able to roll back even
// when the table cannot be written.
func (s *Store) RestoreGrants(snap GrantSnapshot) error {
	if snap.rootPath == "" {
		return fmt.Errorf("routing: restoring an empty snapshot")
	}
	if current, err := readTableFile(s.path); err == nil {
		entries := map[string]projectRecord{}
		for tok, rec := range current.Projects {
			if rec.RootPath == snap.rootPath {
				entries[tok] = rec
			}
		}
		if maps.Equal(entries, snap.entries) {
			return nil
		}
	}
	return s.update(func(f *tableFile) {
		for tok, rec := range f.Projects {
			if rec.RootPath == snap.rootPath {
				delete(f.Projects, tok)
			}
		}
		for tok, rec := range snap.entries {
			f.Projects[tok] = rec
		}
	})
}

// Pause suspends recording for every project without touching any
// grant. The reason records who paused so that only the matching
// Resume lifts it.
func (s *Store) Pause(reason PauseReason) error {
	if reason == "" {
		return fmt.Errorf("routing: pause requires a reason")
	}
	return s.update(func(f *tableFile) { f.PausedReason, f.PausedByVersion = reason, "" })
}

// PauseByBuild is Pause with the build that paused written down, for a
// pause that a different build is expected to lift: what one build
// cannot handle, a later one may. Every path that stops recording for
// such a reason pauses through here, so the build is recorded the same
// way whichever path noticed.
func (s *Store) PauseByBuild(reason PauseReason, version string) error {
	if reason == "" {
		return fmt.Errorf("routing: pause requires a reason")
	}
	return s.update(func(f *tableFile) { f.PausedReason, f.PausedByVersion = reason, version })
}

// ResumeOtherBuild lifts a pause of the given reason once a build other
// than the one that set it is running: the pause waits for a build that
// covers what stopped recording, and the running build is that one
// exactly when it is not the recorded one. The same build lifts
// nothing — it would stop on the same records again. A pause that
// recorded no build was set by a build that is not this one and is
// lifted as well. pausedBy names the build of a lifted pause, for a
// surface to state; it is empty when nothing was lifted. A table with
// no such pause is left untouched, missing file included.
func (s *Store) ResumeOtherBuild(reason PauseReason, running string) (resumed bool, pausedBy string, err error) {
	f, err := readTableFile(s.path)
	if err != nil {
		return false, "", err
	}
	if !liftDue(f, reason, running) {
		return false, "", nil
	}
	err = s.update(func(f *tableFile) {
		if !liftDue(*f, reason, running) {
			return
		}
		resumed, pausedBy = true, namedBuild(f.PausedByVersion)
		f.PausedReason, f.PausedByVersion = "", ""
	})
	if err != nil {
		return false, "", err
	}
	return resumed, pausedBy, nil
}

// liftDue reports that the standing pause is the given reason and that
// the build running now is not the one that set it. It is the one
// comparison of recorded build against running build there is.
func liftDue(f tableFile, reason PauseReason, running string) bool {
	return f.PausedReason == reason && f.PausedByVersion != running
}

// namedBuild states which build a pause records: the version it wrote
// down, or what is known about a pause that wrote none.
func namedBuild(version string) string {
	if version == "" {
		return "an earlier build"
	}
	return "version " + version
}

// Resume lifts a pause set for the given reason. A pause held for a
// different reason is left in place.
func (s *Store) Resume(reason PauseReason) error {
	return s.update(func(f *tableFile) {
		if f.PausedReason == reason {
			f.PausedReason, f.PausedByVersion = "", ""
		}
	})
}

// PausedReason reports the active device-wide pause, empty when
// recording is not paused.
func (s *Store) PausedReason() (PauseReason, error) {
	f, err := readTableFile(s.path)
	if err != nil {
		return "", err
	}
	return f.PausedReason, nil
}

// Resolve answers, for one token, the question the proxy asks of its
// cached view of the table before it records anything: may this be
// recorded, and when not, why. It reads the table as it stands, so a
// command that records reaches the same verdict the proxy would, and a
// device-wide pause is not something each recording path decides for
// itself.
func (s *Store) Resolve(token string) (Verdict, error) {
	f, err := readTableFile(s.path)
	if err != nil {
		return Verdict{}, err
	}
	rec, known := f.Projects[token]
	return verdictFor(known, rec.RevokedAt != "", f.PausedReason), nil
}

// Records reports whether this device may record for token right now.
// Every recording path off the proxy's cached table asks this one
// question before it opens the spool, so a device-wide pause stops all
// of them at once and a reason that means "forward but do not record"
// takes effect on each of them without a second edit. A table that
// cannot be read answers no: refusing beats guessing.
func (s *Store) Records(token string) bool {
	verdict, err := s.Resolve(token)
	return err == nil && verdict.Records()
}

// Active returns the standing grant for rootPath.
func (s *Store) Active(rootPath string) (Grant, bool, error) {
	grants, err := s.All()
	if err != nil {
		return Grant{}, false, err
	}
	for _, g := range grants {
		if g.RootPath == rootPath && !g.Revoked {
			return g, true, nil
		}
	}
	return Grant{}, false, nil
}

// All returns every grant, revoked ones included, so callers like
// uninstall can find each project that ever received an injection.
func (s *Store) All() ([]Grant, error) {
	f, err := readTableFile(s.path)
	if err != nil {
		return nil, err
	}
	grants := make([]Grant, 0, len(f.Projects))
	for tok, rec := range f.Projects {
		shape := WithProxy
		if rec.NoProxy {
			shape = WithoutProxy
		}
		g := Grant{
			Token:          tok,
			ProjectIDHash:  rec.ProjectIDHash,
			RootPath:       rec.RootPath,
			Upstream:       rec.Upstream,
			GrantedAt:      rec.GrantedAt,
			Revoked:        rec.RevokedAt != "",
			Shape:          shape,
			EarlierSkipped: rec.NoEarlier,
		}
		if rec.UpstreamMoved != nil {
			g.UpstreamMoved = UpstreamMove{From: rec.UpstreamMoved.From, At: rec.UpstreamMoved.At}
		}
		grants = append(grants, g)
	}
	return grants, nil
}

// update rewrites the table under fsatomic's cross-process lock: enable
// hooks and commands run as concurrent short-lived processes, and one
// project's grant must never be lost to another's.
func (s *Store) update(mutate func(*tableFile)) error {
	if err := userdirs.EnsureOwnerDir(filepath.Dir(s.path)); err != nil {
		return err
	}
	return fsatomic.Update(s.path, 0o600, func(old []byte) ([]byte, error) {
		f, err := parseTableFile(s.path, old)
		if err != nil {
			return nil, err
		}
		mutate(&f)
		data, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(data, '\n'), nil
	})
}

// UnreadableError is a table that cannot be read or parsed. The store
// never returns one for a missing table: to the store, that is a device
// where nothing was enabled. The proxy's Table does, and where traffic
// goes while the table cannot be read is stated once, on Table. The
// error is what lets a surface say that nothing is recorded instead of
// reporting a device with nothing enabled.
type UnreadableError struct {
	Path string
	Err  error
}

func (e *UnreadableError) Error() string {
	return fmt.Sprintf("routing: reading %s: %v", e.Path, e.Err)
}

func (e *UnreadableError) Unwrap() error { return e.Err }

// readTableFile loads the on-disk table; a missing file is the normal
// nothing-enabled state and yields an empty table. The read goes
// through fsatomic so it neither blocks nor is broken by a concurrent
// store's rewrite on Windows.
func readTableFile(path string) (tableFile, error) {
	data, err := fsatomic.ReadFile(path)
	if os.IsNotExist(err) {
		data = nil
	} else if err != nil {
		return tableFile{Projects: map[string]projectRecord{}}, &UnreadableError{Path: path, Err: err}
	}
	return parseTableFile(path, data)
}

func parseTableFile(path string, data []byte) (tableFile, error) {
	f := tableFile{Projects: map[string]projectRecord{}}
	if len(data) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, &UnreadableError{Path: path, Err: err}
	}
	if f.Projects == nil {
		f.Projects = map[string]projectRecord{}
	}
	return f, nil
}
