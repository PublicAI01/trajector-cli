// Package consent records the user's data-contribution decisions: the
// accepted agreement version and each project's granted or denied
// state. The store is the durable record of intent; the routing table
// is derived from it and can always be rebuilt against it.
package consent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// SetProjectState records a project's decision.
func (s *Store) SetProjectState(projectIDHash, rootPath string, state ProjectState, at string) error {
	return s.update(func(f *storeFile) {
		f.Projects[projectIDHash] = projectRecord{
			RootPath:  rootPath,
			State:     state,
			UpdatedAt: at,
		}
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
	f := storeFile{Projects: map[string]projectRecord{}}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("consent: parsing %s: %w", s.path, err)
	}
	if f.Projects == nil {
		f.Projects = map[string]projectRecord{}
	}
	return f, nil
}

func (s *Store) update(mutate func(*storeFile)) error {
	f, err := s.read()
	if err != nil {
		return err
	}
	mutate(&f)
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := userdirs.EnsureOwnerDir(filepath.Dir(s.path)); err != nil {
		return err
	}
	return fsatomic.WriteFile(s.path, data, 0o600)
}
