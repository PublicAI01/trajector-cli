package proxytest

import (
	"slices"

	"github.com/PublicAI01/trajector-cli/internal/drift"
	"github.com/PublicAI01/trajector-cli/internal/follow"
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

// RetireSessionFile stops one entry from ever being read again, as a
// reader that found the session outside what consent covers does. The
// entry stays in the registry, which is what retirement means.
func (s *Sandbox) RetireSessionFile(projectIDHash, path string) {
	s.t.Helper()
	f := s.registered(projectIDHash, path)
	if err := s.registry().Retire(projectIDHash, f, f, follow.Relocated); err != nil {
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
