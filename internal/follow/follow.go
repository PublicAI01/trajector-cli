// Package follow keeps the registry of files trajector reads for an
// enabled project, and how far each has been read. The layout is a
// documented product contract:
//
//	<dir>/<project-id-hash>.json   one registry per enabled project
//
// A registry is one JSON object: {"version":1,"files":[...]}, each
// element carrying path, inode, size, offset, next_segment, and
// message_ids, and optionally read_at and subpath. The path is the
// entry's identity. Inode, size, and offset describe the file as it
// was last observed locally: they steer reading and never leave it.
// An optional "gaps" object records what the one-time search for the
// project's earlier files could not cover, so a later reading of the
// registry can say so without searching again. An optional "signals"
// object accumulates what the reader noticed about the shape of the
// lines it read, as counts and field names only, so a surface can
// state it without opening a session file. Directories are 0700
// and files 0600: the paths alone reveal what the user works on.
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
	// Subpath is where the session ran relative to the project root,
	// with forward slashes. It is absent, never empty, for a session
	// that ran at the root and for a file the backfill walk found,
	// which carries no such position.
	Subpath string `json:"subpath,omitempty"`
	// MessageIDs are the message ids already consumed from this file.
	// It is a set; the array order carries no meaning. An agent
	// metadata file has no messages: there it holds the record id of
	// the last snapshot taken.
	MessageIDs []string `json:"message_ids"`
	// ReadAt is when the file was last read, in RFC 3339, and absent
	// for a file never read. The reader writes it with the cursor; the
	// registry only keeps it.
	ReadAt string `json:"read_at,omitempty"`
}

// MainSession reports whether f is a session's own file rather than
// an agent file kept beside it under subagents/. Counting sessions
// means counting these.
func (f File) MainSession() bool {
	_, file := identify(f.Path)
	return file == "" && strings.HasSuffix(f.Path, linesExt)
}

// Ambiguity is a directory whose session files were not registered
// because Claude Code stores them under a name that at least one
// other real directory shares, so they cannot be attributed to one
// working directory.
type Ambiguity struct {
	// Dir is the directory under the project root that was skipped.
	Dir string `json:"dir"`
	// Name is the stored name Dir shares with the other directories.
	Name string `json:"name"`
	// Matches lists every real directory that stores under Name,
	// sorted. Dir is among them when it was found.
	Matches []string `json:"matches"`
}

// Gaps is what the search for a project's earlier session files
// could not cover. It is recorded with the registry so the outcome
// of the one search that ran can be shown afterwards without running
// another.
type Gaps struct {
	// Truncated reports that the project's directory tree was larger
	// than the search visits, so directories past its limit were not
	// looked at.
	Truncated bool `json:"truncated,omitempty"`
	// Ambiguous lists the directories skipped for a shared name.
	Ambiguous []Ambiguity `json:"ambiguous,omitempty"`
	// Unreadable lists the directories whose entries could not be
	// listed; directories below them were not looked at.
	Unreadable []string `json:"unreadable,omitempty"`
}

// Any reports whether anything was left uncovered.
func (g Gaps) Any() bool {
	return g.Truncated || len(g.Ambiguous) > 0 || len(g.Unreadable) > 0
}

// Signals is what the reader noticed about the shape of the lines it
// read for a project, kept with the registry so it can be shown
// without reading the files again. Counts only ever grow, and a list
// holds each name once: the reader adds what one read found, and the
// registry sums it with what earlier reads found. It carries no path
// of the user's, no session id, and no value from a line — field
// names and counts are all a surface may print.
type Signals struct {
	// UnanchoredPathFields are the JSON paths of fields found holding
	// an absolute path that this build does not know how to mask.
	UnanchoredPathFields []string `json:"unanchored_path_fields,omitempty"`
	// IncompleteSegments counts the reads whose lines did not end in a
	// newline.
	IncompleteSegments int `json:"incomplete_segments,omitempty"`

	AssistantLines                 int `json:"assistant_lines,omitempty"`
	AssistantLinesWithoutMessageID int `json:"assistant_lines_without_message_id,omitempty"`
	MessagesWithBlockIndexGap      int `json:"messages_with_block_index_gap,omitempty"`
	AgentLines                     int `json:"agent_lines,omitempty"`
	AgentLinesWithoutParent        int `json:"agent_lines_without_parent,omitempty"`

	NewLaunchSurfaces  []string `json:"new_launch_surfaces,omitempty"`
	NewAttachmentTypes []string `json:"new_attachment_types,omitempty"`
	NewSystemSubtypes  []string `json:"new_system_subtypes,omitempty"`
	NewTopLevelTypes   []string `json:"new_top_level_types,omitempty"`
}

// Any reports whether anything was ever noticed.
func (s Signals) Any() bool {
	return len(s.UnanchoredPathFields) > 0 || s.IncompleteSegments > 0 ||
		s.AssistantLines > 0 || s.AgentLines > 0 ||
		len(s.NewLaunchSurfaces) > 0 || len(s.NewAttachmentTypes) > 0 ||
		len(s.NewSystemSubtypes) > 0 || len(s.NewTopLevelTypes) > 0
}

// add sums another read's signals into s.
func (s *Signals) add(more Signals) {
	s.UnanchoredPathFields = union(s.UnanchoredPathFields, more.UnanchoredPathFields)
	s.IncompleteSegments += more.IncompleteSegments
	s.AssistantLines += more.AssistantLines
	s.AssistantLinesWithoutMessageID += more.AssistantLinesWithoutMessageID
	s.MessagesWithBlockIndexGap += more.MessagesWithBlockIndexGap
	s.AgentLines += more.AgentLines
	s.AgentLinesWithoutParent += more.AgentLinesWithoutParent
	s.NewLaunchSurfaces = union(s.NewLaunchSurfaces, more.NewLaunchSurfaces)
	s.NewAttachmentTypes = union(s.NewAttachmentTypes, more.NewAttachmentTypes)
	s.NewSystemSubtypes = union(s.NewSystemSubtypes, more.NewSystemSubtypes)
	s.NewTopLevelTypes = union(s.NewTopLevelTypes, more.NewTopLevelTypes)
}

// union merges two name lists into one sorted list without repeats,
// nil when both are empty.
func union(a, b []string) []string {
	if len(a)+len(b) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(a)+len(b))
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		seen[v] = true
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

type registry struct {
	Version int      `json:"version"`
	Files   []File   `json:"files"`
	Gaps    *Gaps    `json:"gaps,omitempty"`
	Signals *Signals `json:"signals,omitempty"`
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
// one that knows where it ran. The path must be absolute: it is the
// entry's identity across processes with different working directories.
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
// project's files outlives the entries for the files themselves.
func (r *Registry) AddSignals(projectIDHash string, more Signals) error {
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
		if reg.Signals == nil {
			reg.Signals = &Signals{}
		}
		reg.Signals.add(more)
		if !reg.Signals.Any() {
			reg.Signals = nil
		}
		return encode(reg)
	})
}

// Signals reports everything the reader noticed about projectIDHash's
// files so far. A project with no registry, or one whose reads noticed
// nothing, has none.
func (r *Registry) Signals(projectIDHash string) (Signals, error) {
	reg, err := r.read(projectIDHash)
	if err != nil {
		return Signals{}, err
	}
	if reg.Signals == nil {
		return Signals{}, nil
	}
	return *reg.Signals, nil
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

// Remove drops one file's entry from projectIDHash's registry, leaving
// the project's other entries in place. A path that is not registered,
// or a project with no registry, is already in the wanted state. It is
// how a caller retires a file whose cursor can no longer advance: one
// that vanished, or one whose session left the consented directory.
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
