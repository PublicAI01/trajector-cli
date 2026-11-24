package proxytest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/harness/registryv1"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
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

// NameSession records that a session hook named the session path
// belongs to at at, as a hook of a session whose process it could not
// tell does.
func (s *Sandbox) NameSession(projectIDHash, path string, at time.Time) {
	s.t.Helper()
	if err := s.registry().Warm(projectIDHash, path, 0, at); err != nil {
		s.t.Fatalf("name the session of %s: %v", path, err)
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

// RefuseNewEntries makes a project's registry directory refuse every
// new file until the test ends, so a registration fails as one on a
// file system that cannot place an entry does; what is there stays
// readable. Permission bits are what refuses, so the test is skipped
// where they do not: on Windows, and for root.
func (s *Sandbox) RefuseNewEntries(projectIDHash string) {
	s.t.Helper()
	if runtime.GOOS == "windows" {
		s.t.Skip("permission bits do not block writes on windows")
	}
	if os.Geteuid() == 0 {
		s.t.Skip("root writes into every directory")
	}
	dir := filepath.Join(s.layout.FollowDir(), projectIDHash)
	if err := os.Chmod(dir, 0o500); err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// RegistryDirNames lists the names in a project's registry directory,
// so a test can show that a check left nothing there.
func (s *Sandbox) RegistryDirNames(projectIDHash string) []string {
	s.t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.layout.FollowDir(), projectIDHash))
	if err != nil {
		s.t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// RewindCursor puts one entry back to where a reader that kept no
// segment bound left it when it was killed after it stored a segment
// but before it advanced the cursor: the file is still registered,
// under the subpath it had, and nothing counts as read from it. The
// entry file is removed and the path registered again, which is how a
// cursor from byte 0 comes to stand.
func (s *Sandbox) RewindCursor(projectIDHash, path string) {
	s.t.Helper()
	was := s.registered(projectIDHash, path)
	if err := os.Remove(s.entryFile(projectIDHash, path)); err != nil {
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
// empty and registered only for this read. It is then removed, and a
// second read drops its entry as a reader drops the entry of every file
// gone for good, which keeps what it noticed in the project file; the
// project's entries are as they were.
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
	if err := os.Remove(standIn); err != nil {
		s.t.Fatal(err)
	}
	reader.Advance(standIn)
	if slices.Contains(s.RegisteredPaths(projectIDHash), standIn) {
		s.t.Fatal("proxytest: the stand-in's entry outlived its file")
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

// EarlierRegistry and EarlierEntry are a project's registry and one of
// its entries as builds before the per-entry layout wrote them.
type (
	EarlierRegistry = registryv1.Registry
	EarlierEntry    = registryv1.Entry
)

// LeaveEarlierRegistry puts a project's registry on disk as a build
// before the per-entry layout left it.
func (s *Sandbox) LeaveEarlierRegistry(projectIDHash string, reg EarlierRegistry) {
	s.t.Helper()
	if err := userdirs.EnsureOwnerDir(s.layout.FollowDir()); err != nil {
		s.t.Fatal(err)
	}
	if err := registryv1.Write(s.layout.FollowDir(), projectIDHash, reg); err != nil {
		s.t.Fatal(err)
	}
}

// LeaveRegistryHalfwayMigrated puts a project's registry on disk as a
// migration from reg killed right after it rewrote the project file
// leaves it.
func (s *Sandbox) LeaveRegistryHalfwayMigrated(projectIDHash string, reg EarlierRegistry) {
	s.t.Helper()
	if err := userdirs.EnsureOwnerDir(s.layout.FollowDir()); err != nil {
		s.t.Fatal(err)
	}
	if err := registryv1.LeaveHalfway(s.layout.FollowDir(), projectIDHash, reg); err != nil {
		s.t.Fatal(err)
	}
}

// MigratingElsewhere holds, until the test ends or release is called,
// the locks a process in the middle of migrating a project holds: the
// migration's own and the project file's.
func (s *Sandbox) MigratingElsewhere(projectIDHash string) (release func()) {
	s.t.Helper()
	migration, err := registryv1.HoldMigration(s.layout.FollowDir(), projectIDHash)
	if err != nil {
		s.t.Fatal(err)
	}
	projectFile, err := registryv1.HoldProjectFile(s.layout.FollowDir(), projectIDHash)
	if err != nil {
		migration()
		s.t.Fatal(err)
	}
	release = sync.OnceFunc(func() {
		projectFile()
		migration()
	})
	s.t.Cleanup(release)
	return release
}

// RegistryVersion is the layout version a project's project file
// states.
func (s *Sandbox) RegistryVersion(projectIDHash string) int {
	s.t.Helper()
	data, err := fsatomic.ReadFile(filepath.Join(s.layout.FollowDir(), projectIDHash+".json"))
	if err != nil {
		s.t.Fatal(err)
	}
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		s.t.Fatalf("project file %s: %v", data, err)
	}
	return head.Version
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

// BreakRegistryEntry replaces path's entry with bytes that do not
// parse, as a write cut short leaves it.
func (s *Sandbox) BreakRegistryEntry(projectIDHash, path string) {
	s.t.Helper()
	if err := fsatomic.WriteFile(s.entryFile(projectIDHash, path), []byte(`{"path":`), 0o600); err != nil {
		s.t.Fatal(err)
	}
}
