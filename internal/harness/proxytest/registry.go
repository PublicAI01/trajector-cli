package proxytest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
	if err := s.registry().RegisterAll(projectIDHash, []string{path}, subpath); err != nil {
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

// RegistryOnDisk reports a project's registry as it stands on disk,
// every file of it by name and byte for byte, so a test can show that
// a read-only surface wrote nothing to it. A surface that may mark
// sessions hot is checked through RegisteredFiles instead: marking
// writes the heat of a session.
func (s *Sandbox) RegistryOnDisk(projectIDHash string) string {
	s.t.Helper()
	var dump strings.Builder
	add := func(name string) {
		data, err := fsatomic.ReadFile(filepath.Join(s.layout.FollowDir(), name))
		if err != nil {
			s.t.Fatal(err)
		}
		fmt.Fprintf(&dump, "%s\n%s\n", name, data)
	}
	add(projectIDHash + ".json")
	entries, err := os.ReadDir(filepath.Join(s.layout.FollowDir(), projectIDHash))
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	for _, e := range entries {
		add(filepath.Join(projectIDHash, e.Name()))
	}
	return dump.String()
}

// RewindCursor puts one entry back to where a reader that kept no
// segment bound left it when it was killed after it stored a segment
// but before it advanced the cursor: the file is still registered,
// under the subpath it had, and nothing counts as read from it.
func (s *Sandbox) RewindCursor(projectIDHash, path string) {
	s.t.Helper()
	was := s.registered(projectIDHash, path)
	if err := s.registry().Remove(projectIDHash, path); err != nil {
		s.t.Fatal(err)
	}
	if err := s.registry().RegisterAll(projectIDHash, []string{path}, was.Subpath); err != nil {
		s.t.Fatal(err)
	}
}

// ReadToItsEnd moves one entry's cursor to the end of its file as a
// reader does, with consent covering root, and stores nothing of what
// it read: what a test needs is an entry read before, not its records.
func (s *Sandbox) ReadToItsEnd(projectIDHash, root, path string) {
	s.t.Helper()
	s.registered(projectIDHash, path)
	reader := follow.Reader{
		Registry:      s.registry(),
		ProjectIDHash: projectIDHash,
		Options:       follow.ReadOptions{Root: root},
		Store:         func(follow.ReadResult) (follow.Storing, Signals) { return follow.Stored, Signals{} },
	}
	reader.Advance(path)
	st, err := follow.StatFile(path)
	if err != nil {
		s.t.Fatal(err)
	}
	if f := s.registered(projectIDHash, path); f.Offset != st.Size {
		s.t.Fatalf("proxytest: %s read to offset %d of %d", path, f.Offset, st.Size)
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
// would have recorded it: a reader keeps what its store noticed with
// the cursor of the file it read. The file read here is a stand-in,
// empty and registered only for this read, and its entry is removed
// afterwards, so the project's entries are as they were.
func (s *Sandbox) AddSignals(projectIDHash string, more Signals) {
	s.t.Helper()
	dir := s.t.TempDir()
	standIn := filepath.Join(dir, "signals-stand-in.jsonl")
	if err := fsatomic.WriteFile(standIn, nil, 0o600); err != nil {
		s.t.Fatal(err)
	}
	if err := s.registry().RegisterAll(projectIDHash, []string{standIn}, ""); err != nil {
		s.t.Fatal(err)
	}
	reader := follow.Reader{
		Registry:      s.registry(),
		ProjectIDHash: projectIDHash,
		Options:       follow.ReadOptions{Root: dir},
		Store:         func(follow.ReadResult) (follow.Storing, Signals) { return follow.Stored, more },
	}
	reader.Advance(standIn)
	if err := s.registry().Remove(projectIDHash, standIn); err != nil {
		s.t.Fatal(err)
	}
	if !s.Signals(projectIDHash).Any() && more.Any() {
		s.t.Fatal("proxytest: the stand-in read kept no signals")
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

// entryFile is the file a project's registry keeps path's entry in.
func (s *Sandbox) entryFile(projectIDHash, path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(s.layout.FollowDir(), projectIDHash, hex.EncodeToString(sum[:16])+".json")
}

// NeedALaterBuild marks path's entry as one only a build that knows
// need can read, as a later build that writes such an entry would.
func (s *Sandbox) NeedALaterBuild(projectIDHash, path, need string) {
	s.t.Helper()
	file := s.entryFile(projectIDHash, path)
	data, err := fsatomic.ReadFile(file)
	if err != nil {
		s.t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		s.t.Fatal(err)
	}
	entry["needs"] = []string{need}
	if data, err = json.Marshal(entry); err != nil {
		s.t.Fatal(err)
	}
	if err := fsatomic.WriteFile(file, data, 0o600); err != nil {
		s.t.Fatal(err)
	}
}
