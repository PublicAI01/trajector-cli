// Package follow keeps the registry of files trajector reads for an
// enabled project, and how far each has been read. The layout is a
// documented product contract:
//
//	<dir>/<project-id-hash>.json   one registry per enabled project
//
// A registry is one JSON object: {"version":1,"files":[...]}, each
// element carrying path, inode, size, offset, next_segment, and
// message_ids. The path is the entry's identity. Inode, size, and
// offset describe the file as it was last observed locally:
// they steer reading and never leave it. Directories are 0700 and
// files 0600: the paths alone reveal what the user works on.
//
// Reading is a function of one registered entry and the file on disk:
// Read consumes the complete lines a file gained since its cursor and
// hands back the records to store together with the advanced cursor.
// The registry decides nothing about content; nothing here writes a
// file it reads.
package follow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// Registry is the on-disk set of files trajector reads for an enabled
// project, and how far each has been read.
type Registry struct{ dir string }

// Open prepares the registry rooted at dir without creating anything.
// The directory is created, owner-only, by the first registration.
func Open(dir string) *Registry { return &Registry{dir: dir} }

// File is one registered file and its cursor.
type File struct {
	Path  string `json:"path"`
	Inode uint64 `json:"inode"`
	Size  int64  `json:"size"`
	// Offset is the byte position reading stopped at.
	Offset int64 `json:"offset"`
	// NextSegment only grows. It survives a Rewrite of the file, so it
	// is stored rather than derived: nothing on disk can give it back.
	NextSegment int `json:"next_segment"`
	// MessageIDs are the message ids already consumed from this file.
	// It is a set; the array order carries no meaning. An agent
	// metadata file has no messages: there it holds the record id of
	// the last snapshot taken.
	MessageIDs []string `json:"message_ids"`
}

type registry struct {
	Version int    `json:"version"`
	Files   []File `json:"files"`
}

// Register adds path to projectIDHash's registry with a zero cursor.
// Registering a path that is already registered changes nothing. The path must
// be absolute: it is the entry's identity across processes with
// different working directories.
func (r *Registry) Register(projectIDHash, path string) error {
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
			reg.Files = append(reg.Files, File{Path: path})
		}
		return encode(reg)
	})
}

// Registered reports whether path is in projectIDHash's registry.
func (r *Registry) Registered(projectIDHash, path string) (bool, error) {
	reg, err := r.read(projectIDHash)
	if err != nil {
		return false, err
	}
	_, ok := find(reg.Files, path)
	return ok, nil
}

// Files lists projectIDHash's registered files, ordered by path. A
// project with no registry has no files.
func (r *Registry) Files(projectIDHash string) ([]File, error) {
	reg, err := r.read(projectIDHash)
	if err != nil {
		return nil, err
	}
	files := append([]File{}, reg.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// Update replaces the whole entry whose path is f.Path.
func (r *Registry) Update(projectIDHash string, f File) error {
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
		i, ok := find(reg.Files, f.Path)
		if !ok {
			return nil, ErrNotRegistered
		}
		reg.Files[i] = f
		return encode(reg)
	})
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
	return reg, nil
}

func encode(reg registry) ([]byte, error) {
	if reg.Files == nil {
		reg.Files = []File{}
	}
	for i := range reg.Files {
		if reg.Files[i].MessageIDs == nil {
			reg.Files[i].MessageIDs = []string{}
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
