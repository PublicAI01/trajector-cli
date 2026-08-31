// Package consent records the user's data-contribution decisions: the
// accepted agreement version and each project's granted or denied
// state. The store is the durable audit record of intent and the gate
// enable checks before granting. It is a record, not a source: the
// credentials a grant runs on — the project's token and upstream —
// live only in the routing table, so this store can attest to a
// decision but never reissue its grant.
package consent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// ProjectState is a project's recorded decision.
type ProjectState string

const (
	StateGranted ProjectState = "granted"
	StateDenied  ProjectState = "denied"
)

type storeFile struct {
	Agreement agreementRecord          `json:"agreement"`
	Projects  map[string]projectRecord `json:"projects"`
	// Prompted lists the projects that have already seen the one-time
	// onboarding hint, by hash only.
	Prompted []string `json:"prompted,omitempty"`
}

type agreementRecord struct {
	Version    string `json:"version,omitempty"`
	AcceptedAt string `json:"accepted_at,omitempty"`
}

type projectRecord struct {
	RootPath  string       `json:"root_path"`
	State     ProjectState `json:"state"`
	UpdatedAt string       `json:"updated_at"`
	// Settings is absent in store files written before optional
	// settings existed; both readers treat absence as "no decisions".
	Settings map[string]SettingDecision `json:"settings,omitempty"`
}

// Store reads and writes the consent file. Every method loads the
// current file so concurrent CLI invocations cannot clobber each other
// with stale in-memory state.
type Store struct {
	path string
}

// Open returns the store backed by the consent file at path.
func Open(path string) *Store { return &Store{path: path} }

// Path reports the store's file location.
func (s *Store) Path() string { return s.path }

// AcceptedVersion reports the agreement version the user accepted and
// when; both are empty when no agreement was ever accepted.
func (s *Store) AcceptedVersion() (version, acceptedAt string, err error) {
	f, err := s.read()
	if err != nil {
		return "", "", err
	}
	return f.Agreement.Version, f.Agreement.AcceptedAt, nil
}

// AcceptAgreement records explicit acceptance of an agreement version.
func (s *Store) AcceptAgreement(version, at string) error {
	if version == "" {
		return fmt.Errorf("consent: agreement version is required")
	}
	return s.update(func(f *storeFile) {
		f.Agreement = agreementRecord{Version: version, AcceptedAt: at}
	})
}

// ProjectState reports a project's recorded decision.
func (s *Store) ProjectState(projectIDHash string) (ProjectState, bool, error) {
	f, err := s.read()
	if err != nil {
		return "", false, err
	}
	rec, ok := f.Projects[projectIDHash]
	return rec.State, ok, nil
}

// SetProjectState records a project's decision. Setting decisions are
// kept: a declined answer must survive disable/enable cycles so one
// refusal keeps ending the ask.
func (s *Store) SetProjectState(projectIDHash, rootPath string, state ProjectState, at string) error {
	return s.update(func(f *storeFile) {
		f.Projects[projectIDHash] = projectRecord{
			RootPath:  rootPath,
			State:     state,
			UpdatedAt: at,
			Settings:  f.Projects[projectIDHash].Settings,
		}
	})
}

// ProjectSnapshot captures one project's recorded decision so a failed
// enable can put exactly it back. The value is opaque: only the store
// that minted it can restore it.
type ProjectSnapshot struct {
	hash string
	rec  *projectRecord
}

// SnapshotProject captures the project's current record; a project with
// no record yields a snapshot that restores to absence.
func (s *Store) SnapshotProject(projectIDHash string) (ProjectSnapshot, error) {
	if projectIDHash == "" {
		return ProjectSnapshot{}, fmt.Errorf("consent: project hash is required")
	}
	f, err := s.read()
	if err != nil {
		return ProjectSnapshot{}, err
	}
	snap := ProjectSnapshot{hash: projectIDHash}
	if rec, ok := f.Projects[projectIDHash]; ok {
		snap.rec = &rec
	}
	return snap, nil
}

// RestoreProject puts the snapshotted project back to its captured
// state. Only that project's record is touched: the restore runs under
// the same serialized update as every other mutation, so a concurrent
// process's decision about another project is never lost to a rollback.
// A store already matching the snapshot is left alone entirely — an
// enable that failed before writing must be able to roll back even
// when the store cannot be written.
func (s *Store) RestoreProject(snap ProjectSnapshot) error {
	if snap.hash == "" {
		return fmt.Errorf("consent: restoring an empty snapshot")
	}
	if current, err := s.read(); err == nil {
		rec, ok := current.Projects[snap.hash]
		if !ok && snap.rec == nil {
			return nil
		}
		if ok && snap.rec != nil && reflect.DeepEqual(rec, *snap.rec) {
			return nil
		}
	}
	return s.update(func(f *storeFile) {
		if snap.rec == nil {
			delete(f.Projects, snap.hash)
			return
		}
		f.Projects[snap.hash] = *snap.rec
	})
}

// MarkPrompted records that a project has seen the one-time onboarding
// hint and reports whether this call was the first. Taking only a hash
// is the guarantee: the hint can never teach this file a project path.
func (s *Store) MarkPrompted(projectIDHash string) (first bool, err error) {
	if projectIDHash == "" {
		return false, fmt.Errorf("consent: project hash is required")
	}
	err = s.update(func(f *storeFile) {
		for _, h := range f.Prompted {
			if h == projectIDHash {
				return
			}
		}
		first = true
		f.Prompted = append(f.Prompted, projectIDHash)
		sort.Strings(f.Prompted)
	})
	if err != nil {
		return false, err
	}
	return first, nil
}

func (s *Store) read() (storeFile, error) {
	data, err := fsatomic.ReadFile(s.path)
	if os.IsNotExist(err) {
		data = nil
	} else if err != nil {
		return storeFile{Projects: map[string]projectRecord{}}, err
	}
	return parseStoreFile(s.path, data)
}

func parseStoreFile(path string, data []byte) (storeFile, error) {
	f := storeFile{Projects: map[string]projectRecord{}}
	if len(data) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("consent: parsing %s: %w", path, err)
	}
	if f.Projects == nil {
		f.Projects = map[string]projectRecord{}
	}
	return f, nil
}

// update rewrites the store under fsatomic's cross-process lock, so
// concurrent commands never lose each other's consent decisions.
func (s *Store) update(mutate func(*storeFile)) error {
	if err := userdirs.EnsureOwnerDir(filepath.Dir(s.path)); err != nil {
		return err
	}
	return fsatomic.Update(s.path, 0o600, func(old []byte) ([]byte, error) {
		f, err := parseStoreFile(s.path, old)
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
