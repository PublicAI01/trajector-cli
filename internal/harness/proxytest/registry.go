package proxytest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// RegisteredFile is one file an enabled project reads and how far it
// is read. The harness reads and writes the registry through follow's
// own type: it does not redeclare the contract.
type RegisteredFile = follow.File

// Signals is what reading a project's session files noticed about the
// shape of their lines, in the type of the module that names them.
type Signals = drift.Signals

func (s *Sandbox) registry() *follow.Registry { return follow.Open(s.layout.FollowDir()) }

// RegisterSessionFile puts one file in a project's registry under the
// subpath the session ran in, as a session hook would. An empty
// subpath is a session that ran at the project root.
func (s *Sandbox) RegisterSessionFile(projectIDHash, path, subpath string) {
	s.t.Helper()
	if err := s.registry().RegisterUnder(projectIDHash, path, subpath); err != nil {
		s.t.Fatal(err)
	}
}

// RegisteredFiles reports a project's registered entries, cursors
// included.
func (s *Sandbox) RegisteredFiles(projectIDHash string) []RegisteredFile {
	s.t.Helper()
	files, err := s.registry().Files(projectIDHash)
	if err != nil {
		s.t.Fatal(err)
	}
	return files
}

// RegisteredPaths reports which files a project has registered, in the
// order the registry keeps them.
func (s *Sandbox) RegisteredPaths(projectIDHash string) []string {
	s.t.Helper()
	files := s.RegisteredFiles(projectIDHash)
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}

// RewindCursor puts one entry back to where a reader killed after it
// stored a segment but before it advanced the cursor left it: the file
// is still registered, and nothing counts as read from it.
func (s *Sandbox) RewindCursor(projectIDHash, path string) {
	s.t.Helper()
	from := s.registered(projectIDHash, path)
	f := from
	f.Offset, f.Size, f.Inode, f.NextSegment, f.MessageIDs = 0, 0, 0, 0, nil
	if err := s.registry().Update(projectIDHash, from, f); err != nil {
		s.t.Fatal(err)
	}
}

// RetireAsAnEarlierBuild writes one entry as builds before this one
// left a session that moved out of the project: the cursor stands just
// past the first relocated line of the session file, and the entry
// carries "retired": "relocated". Those builds retired an entry only
// once they had read past that line, so the file must hold one. The
// registry file name and the retirement are spelled here as those
// builds spelled them: what is simulated is bytes on disk, and a
// rename in this build must not move them.
func (s *Sandbox) RetireAsAnEarlierBuild(projectIDHash, path string) {
	s.t.Helper()
	content, err := fsatomic.ReadFile(path)
	if err != nil {
		s.t.Fatal(err)
	}
	offset, found := 0, false
	for line := range bytes.Lines(content) {
		offset += len(line)
		if found = bytes.Contains(line, []byte(`"type":"relocated"`)); found {
			break
		}
	}
	if !found {
		s.t.Fatalf("%s holds no relocated line for an earlier build to have read past", path)
	}
	file := filepath.Join(s.layout.FollowDir(), projectIDHash+".json")
	err = fsatomic.Update(file, 0o600, func(old []byte) ([]byte, error) {
		var reg map[string]any
		if err := json.Unmarshal(old, &reg); err != nil {
			return nil, err
		}
		files, _ := reg["files"].([]any)
		for _, f := range files {
			if entry, ok := f.(map[string]any); ok && entry["path"] == path {
				entry["offset"], entry["size"] = offset, len(content)
				entry["retired"] = "relocated"
				return json.Marshal(reg)
			}
		}
		return nil, fmt.Errorf("no file registered at %s", path)
	})
	if err != nil {
		s.t.Fatal(err)
	}
}

func (s *Sandbox) registered(projectIDHash, path string) RegisteredFile {
	s.t.Helper()
	files := s.RegisteredFiles(projectIDHash)
	i := slices.IndexFunc(files, func(f RegisteredFile) bool { return f.Path == path })
	if i < 0 {
		s.t.Fatalf("no file registered at %s", path)
	}
	return files[i]
}

// ProjectsWithRegistry lists every project this device holds a
// registry for.
func (s *Sandbox) ProjectsWithRegistry() []string {
	s.t.Helper()
	projects, err := s.registry().Projects()
	if err != nil {
		s.t.Fatal(err)
	}
	return projects
}

// AddSignals accumulates what a reading run noticed, as the run itself
// would have recorded it.
func (s *Sandbox) AddSignals(projectIDHash string, more Signals) {
	s.t.Helper()
	if err := s.registry().AddSignals(projectIDHash, more); err != nil {
		s.t.Fatal(err)
	}
}

// Signals reports what a project's registry accumulated.
func (s *Sandbox) Signals(projectIDHash string) Signals {
	s.t.Helper()
	got, err := s.registry().Signals(projectIDHash)
	if err != nil {
		s.t.Fatal(err)
	}
	return got
}
