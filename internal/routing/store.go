package routing

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"

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

// Grant is one project's consent record: which project, where it lives,
// and the token that identifies its traffic. The proxy never sees these
// fields; it only needs a Route.
type Grant struct {
	Token         string
	ProjectIDHash string
	RootPath      string
	Upstream      string
	GrantedAt     string
	// Revoked reports the per-entry revocation only, never a device-wide
	// pause. A caller that wants to know whether traffic is being
	// recorded must ask the Table.
	Revoked bool
}

// Grant installs the record for one project. Any previous entry for the
// same root path — active or revoked — is replaced: a re-enabled
// project rotates its token instead of resurrecting an old one.
func (s *Store) Grant(g Grant) error {
	return s.update(func(f *tableFile) {
		for tok, rec := range f.Projects {
			if rec.RootPath == g.RootPath {
				delete(f.Projects, tok)
			}
		}
		f.Projects[g.Token] = projectRecord{
			ProjectIDHash: g.ProjectIDHash,
			RootPath:      g.RootPath,
			Upstream:      g.Upstream,
			GrantedAt:     g.GrantedAt,
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
func (s *Store) SetUpstream(rootPath, upstream string) error {
	return s.update(func(f *tableFile) {
		for tok, rec := range f.Projects {
			if rec.RootPath == rootPath && rec.RevokedAt == "" {
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
	return s.update(func(f *tableFile) { f.PausedReason = reason })
}

// Resume lifts a pause set for the given reason. A pause held for a
// different reason is left in place.
func (s *Store) Resume(reason PauseReason) error {
	return s.update(func(f *tableFile) {
		if f.PausedReason == reason {
			f.PausedReason = ""
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
		grants = append(grants, Grant{
			Token:         tok,
			ProjectIDHash: rec.ProjectIDHash,
			RootPath:      rec.RootPath,
			Upstream:      rec.Upstream,
			GrantedAt:     rec.GrantedAt,
			Revoked:       rec.RevokedAt != "",
		})
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

// readTableFile loads the on-disk table; a missing file is the normal
// nothing-enabled state and yields an empty table.
func readTableFile(path string) (tableFile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data = nil
	} else if err != nil {
		return tableFile{Projects: map[string]projectRecord{}}, err
	}
	return parseTableFile(path, data)
}

func parseTableFile(path string, data []byte) (tableFile, error) {
	f := tableFile{Projects: map[string]projectRecord{}}
	if len(data) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("routing: parsing %s: %w", path, err)
	}
	if f.Projects == nil {
		f.Projects = map[string]projectRecord{}
	}
	return f, nil
}
