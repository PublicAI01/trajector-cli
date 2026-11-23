// Package registryv1 writes a session file registry the way builds
// before the per-entry layout left it on disk, and acts as one of
// their readers does, so a test can hold this build against what such
// a build leaves behind, and against what a migration from it leaves
// when it is stopped halfway. It spells that layout on its own, on purpose:
// what a test needs is the bytes a released build wrote, and a change
// to the registry package must not move them.
//
// The layout is one file per project, <dir>/<project-id-hash>.json,
// holding {"version":1,"files":[...]}. A reader of one of its files
// holds <dir>/<project-id-hash>.<path-digest>.lock while it reads, and
// a build that kept segment bounds left one beside that lock, as
// <dir>/<project-id-hash>.<path-digest>.bound.
package registryv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// Entry is one file of the registry with its cursor, as an earlier
// build wrote it.
type Entry struct {
	Path                    string   `json:"path"`
	Inode                   uint64   `json:"inode"`
	Size                    int64    `json:"size"`
	Offset                  int64    `json:"offset"`
	NextSegment             int      `json:"next_segment"`
	MessageIDs              []string `json:"message_ids"`
	ReadAt                  string   `json:"read_at,omitempty"`
	Subpath                 string   `json:"subpath,omitempty"`
	Retired                 string   `json:"retired,omitempty"`
	LastEvent               string   `json:"last_event,omitempty"`
	PID                     int      `json:"pid,omitzero"`
	Told                    bool     `json:"told,omitzero"`
	BeforeRewrite           []string `json:"before_rewrite,omitempty"`
	EmptyReasoningBlocks    int      `json:"empty_reasoning_blocks,omitzero"`
	ReasoningBlocksWithText int      `json:"reasoning_blocks_with_text,omitzero"`
	LaunchSurface           string   `json:"launch_surface,omitempty"`
}

// Registry is one project's registry. Gaps and Signals are written as
// whatever JSON their values encode to, and left out when nil.
type Registry struct {
	Files           []Entry `json:"files"`
	Gaps            any     `json:"gaps,omitempty"`
	Signals         any     `json:"signals,omitempty"`
	ReasoningToldOn string  `json:"reasoning_told_on,omitempty"`
}

// onDisk is the file's object: the version comes first, as earlier
// builds wrote it.
type onDisk struct {
	Version int `json:"version"`
	Registry
}

// Path is where the registry of projectIDHash lies under dir.
func Path(dir, projectIDHash string) string {
	return filepath.Join(dir, projectIDHash+".json")
}

// Encode is reg as an earlier build wrote it.
func Encode(reg Registry) ([]byte, error) {
	if reg.Files == nil {
		reg.Files = []Entry{}
	}
	for i := range reg.Files {
		if reg.Files[i].MessageIDs == nil {
			reg.Files[i].MessageIDs = []string{}
		}
	}
	return json.Marshal(onDisk{Version: 1, Registry: reg})
}

// Write puts reg on disk as projectIDHash's registry under dir,
// replacing whatever stood there.
func Write(dir, projectIDHash string, reg Registry) error {
	data, err := Encode(reg)
	if err != nil {
		return err
	}
	return fsatomic.WriteFile(Path(dir, projectIDHash), data, 0o600)
}

// Update rewrites projectIDHash's registry with change applied, under
// the registry's own lock, as an earlier build's reader writes its
// cursor back. A registry that is no longer version 1 is left as it is
// and Update fails, as an earlier build refuses it.
func Update(dir, projectIDHash string, change func(*Registry)) error {
	return fsatomic.Update(Path(dir, projectIDHash), 0o600, func(old []byte) ([]byte, error) {
		var was onDisk
		if err := json.Unmarshal(old, &was); err != nil {
			return nil, err
		}
		if was.Version != 1 {
			return nil, errNotVersion1
		}
		change(&was.Registry)
		return Encode(was.Registry)
	})
}

var errNotVersion1 = errors.New("registryv1: the registry is not version 1")

// lockTarget is the name an earlier build locks one file's entry under.
func lockTarget(dir, projectIDHash, path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(dir, projectIDHash+"."+hex.EncodeToString(sum[:16]))
}

// LockPath is the lock file an earlier build's reader of path holds.
func LockPath(dir, projectIDHash, path string) string {
	return lockTarget(dir, projectIDHash, path) + ".lock"
}

// BoundPath is where a build that kept segment bounds left the bound
// of path's entry.
func BoundPath(dir, projectIDHash, path string) string {
	return lockTarget(dir, projectIDHash, path) + ".bound"
}

// readLockStale is how old an earlier build takes a reader's lock to
// be before it takes the entry from that reader.
const readLockStale = 2 * time.Minute

// HoldEntry takes the lock an earlier build's reader of path takes
// while it reads, waiting up to wait for another holder, and hands
// back the way to let it go.
func HoldEntry(dir, projectIDHash, path string, wait time.Duration) (release func(), err error) {
	return fsatomic.Lock(lockTarget(dir, projectIDHash, path), readLockStale, wait)
}

// Bound is the segment bound a build that kept bounds left for one
// entry.
type Bound struct {
	Offset  int64  `json:"offset"`
	Segment int    `json:"segment"`
	End     int64  `json:"end"`
	Inode   uint64 `json:"inode,omitzero"`
	Rewrite bool   `json:"rewrite,omitzero"`
}

// WriteBound leaves b as the bound of path's entry.
func WriteBound(dir, projectIDHash, path string, b Bound) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return fsatomic.WriteFile(BoundPath(dir, projectIDHash, path), data, 0o600)
}

// LeaveHalfway puts reg on disk as a migration killed right after it
// rewrote the project file leaves it: a project file of the layout
// after this one, and the copy of reg the rest of the migration is
// made from, in the project directory.
func LeaveHalfway(dir, projectIDHash string, reg Registry) error {
	data, err := Encode(reg)
	if err != nil {
		return err
	}
	projectDir := filepath.Join(dir, projectIDHash)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		return err
	}
	if err := fsatomic.WriteFile(filepath.Join(projectDir, "migrating.v1"), data, 0o600); err != nil {
		return err
	}
	return fsatomic.WriteFile(Path(dir, projectIDHash), []byte(`{"version":2,"needs_build":"0.8.0"}`), 0o600)
}

// HoldMigration takes the lock a process migrating projectIDHash holds,
// and hands back the way to let it go.
func HoldMigration(dir, projectIDHash string) (release func(), err error) {
	projectDir := filepath.Join(dir, projectIDHash)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		return nil, err
	}
	return fsatomic.Lock(filepath.Join(projectDir, "migrating.v1"), time.Hour, 0)
}

// HoldProjectFile takes the lock held while projectIDHash's project
// file is rewritten, and hands back the way to let it go.
func HoldProjectFile(dir, projectIDHash string) (release func(), err error) {
	return fsatomic.Lock(Path(dir, projectIDHash), time.Hour, 0)
}
