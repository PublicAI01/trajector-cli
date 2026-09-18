package gitsnapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// Heads remembers, per enabled project, the commit this device last
// observed. It is local state of the same kind as a reading cursor: it
// steers what is observed next and never leaves this machine, because a
// value true of every honest user and forgeable by anyone else says
// nothing to a receiver.
//
// The layout is a documented product contract:
//
//	{"version":1,"heads":{"<project-id-hash>":"<40 hex>"}}
//
// The file is 0600 and every write replaces it whole, so a reader
// either sees the commit before a write or the commit after it.
type Heads struct{ path string }

// headsVersion is the layout written here. A file carrying any other
// version is refused rather than interpreted: a commit read under the
// wrong layout would make the next observation compare against the
// wrong base.
const headsVersion = 1

type headsFile struct {
	Version int               `json:"version"`
	Heads   map[string]string `json:"heads"`
}

// OpenHeads prepares the store at path without creating anything. The
// file is created, owner-only, by the first commit remembered.
func OpenHeads(path string) *Heads { return &Heads{path: path} }

// Last is the commit last observed for a project, and false when this
// device has observed none — which is the first observation of a
// project, and the case where there is nothing to compare against.
func (h *Heads) Last(projectIDHash string) (string, bool) {
	data, err := fsatomic.ReadFile(h.path)
	if err != nil {
		return "", false
	}
	f, err := parseHeads(data)
	if err != nil {
		return "", false
	}
	head, ok := f.Heads[projectIDHash]
	return head, ok && ValidCommitID(head)
}

// See records that this device observed head for a project, replacing
// whatever it remembered before. It refuses anything but a full commit
// identifier: the value is passed back to git later, and a value that
// is not one could be read there as an option.
func (h *Heads) See(projectIDHash, head string) error {
	if projectIDHash == "" {
		return fmt.Errorf("gitsnapshot: a remembered commit needs a project")
	}
	if !ValidCommitID(head) {
		return fmt.Errorf("gitsnapshot: %q is not a commit identifier", head)
	}
	return h.update(func(f *headsFile) { f.Heads[projectIDHash] = head })
}

// Forget drops what this device remembered for a project. A project
// that is no longer enabled is no longer observed, and what was
// remembered about it has nothing left to steer.
func (h *Heads) Forget(projectIDHash string) error {
	if _, err := os.Stat(h.path); os.IsNotExist(err) {
		return nil
	}
	return h.update(func(f *headsFile) { delete(f.Heads, projectIDHash) })
}

func (h *Heads) update(mutate func(*headsFile)) error {
	if err := userdirs.EnsureOwnerDir(filepath.Dir(h.path)); err != nil {
		return err
	}
	return fsatomic.Update(h.path, 0o600, func(old []byte) ([]byte, error) {
		f, err := parseHeads(old)
		if err != nil {
			return nil, err
		}
		mutate(&f)
		return json.Marshal(f)
	})
}

func parseHeads(data []byte) (headsFile, error) {
	f := headsFile{Version: headsVersion, Heads: map[string]string{}}
	if len(data) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return headsFile{}, fmt.Errorf("gitsnapshot: reading remembered commits: %w", err)
	}
	if f.Version != headsVersion {
		return headsFile{}, fmt.Errorf("gitsnapshot: unsupported layout version %d", f.Version)
	}
	if f.Heads == nil {
		f.Heads = map[string]string{}
	}
	return f, nil
}
