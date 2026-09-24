package follow_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
)

func TestMain(m *testing.M) {
	procbin.Main(m, map[string]func(args []string) int{
		"hold": holdWhileReading,
	})
}

// holdWhileReading reads the entry registered for args[1] in the
// registry at args[0], and holds it from inside the store: it creates
// args[2] once the read is handed over, and lets the store answer only
// once args[3] exists.
func holdWhileReading(args []string) int {
	if len(args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: hold <registry-dir> <path> <entered> <release>")
		return 2
	}
	entered, release := args[2], args[3]
	reader := readerWith(follow.Open(args[0]), func(follow.ReadResult) follow.Storing {
		if err := os.WriteFile(entered, nil, 0o600); err != nil {
			return follow.NotStored
		}
		if !appears(release) {
			return follow.NotStored
		}
		return follow.Stored
	})
	reader.Advance(args[1])
	return 0
}

// appears waits for path to exist, and reports whether it did within a
// bound far longer than the test needs.
func appears(path string) bool {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// firstWriterWins keeps segments the way the spool does: a record id
// that is stored already keeps the lines it was stored with, and a
// second write under the same id reports success and changes nothing.
type firstWriterWins struct {
	mu    sync.Mutex
	lines map[string]string
}

func (s *firstWriterWins) store(res follow.ReadResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lines == nil {
		s.lines = map[string]string{}
	}
	for _, seg := range res.Segments {
		if _, ok := s.lines[seg.RecordID]; !ok {
			s.lines[seg.RecordID] = seg.Lines
		}
	}
}

func (s *firstWriterWins) holds(line string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, lines := range s.lines {
		if strings.Contains(lines, line) {
			return true
		}
	}
	return false
}

// readerWith is a reader of project's entries in registry whose store
// is store.
func readerWith(registry *follow.Registry, store func(follow.ReadResult) follow.Storing) follow.Reader {
	return follow.Reader{
		Registry:      registry,
		ProjectIDHash: project,
		Options:       follow.ReadOptions{Root: root},
		Capture:       capture,
		Store:         store,
	}
}

func TestReader_TwoReadersOfOneFileLoseNoLine(t *testing.T) {
	registry := follow.Open(t.TempDir())
	path := mainPath(t)
	first, second := userLine(1), userLine(2)
	writeFile(t, path, first)
	mustRegister(t, registry, project, path)
	var spool firstWriterWins

	shortStored := make(chan struct{})
	longEntered := make(chan struct{})
	shortDone := make(chan struct{})
	longDone := make(chan struct{})
	short := readerWith(registry, func(res follow.ReadResult) follow.Storing {
		spool.store(res)
		close(shortStored)
		select {
		case <-longEntered:
		case <-longDone:
		}
		return follow.Stored
	})
	long := readerWith(registry, func(res follow.ReadResult) follow.Storing {
		close(longEntered)
		<-shortDone
		spool.store(res)
		return follow.Stored
	})

	go func() {
		defer close(shortDone)
		short.Advance(path)
	}()
	<-shortStored
	appendFile(t, path, second)
	go func() {
		defer close(longDone)
		long.Advance(path)
	}()
	<-shortDone
	<-longDone

	last := readerWith(registry, func(res follow.ReadResult) follow.Storing {
		spool.store(res)
		return follow.Stored
	})
	last.Advance(path)

	for i, line := range []string{first, second} {
		if !spool.holds(line) {
			t.Errorf("line %d is in no stored segment, want every line of the file stored", i+1)
		}
	}
}

func TestReader_LeavesAFileAnotherReaderHoldsForTheNextRun(t *testing.T) {
	registry := follow.Open(t.TempDir())
	path := mainPath(t)
	line := userLine(1)
	writeFile(t, path, line)
	mustRegister(t, registry, project, path)

	holderEntered := make(chan struct{})
	waiterDone := make(chan struct{})
	holderDone := make(chan struct{})
	holder := readerWith(registry, func(follow.ReadResult) follow.Storing {
		close(holderEntered)
		<-waiterDone
		return follow.Stored
	})
	waiter := readerWith(registry, func(follow.ReadResult) follow.Storing {
		t.Error("the store was handed a read of a file another reader holds")
		return follow.Stored
	})

	go func() {
		defer close(holderDone)
		holder.Advance(path)
	}()
	<-holderEntered
	next := waiter.Advance(path)
	close(waiterDone)
	<-holderDone

	if !next {
		t.Error("Advance on a held file = false, want the project's next entry still read")
	}
	if f := registeredEntry(t, registry, path); f.Offset != int64(len(line)) || f.NextSegment != 1 {
		t.Errorf("cursor = %+v, want it where the holder moved it", f)
	}
}

func TestReader_LeavesAFileAReaderInAnotherProcessHoldsForTheNextRun(t *testing.T) {
	dir := t.TempDir()
	registryDir := filepath.Join(dir, "follow")
	registry := follow.Open(registryDir)
	path := mainPath(t)
	line := userLine(1)
	writeFile(t, path, line)
	mustRegister(t, registry, project, path)
	entered, release := filepath.Join(dir, "entered"), filepath.Join(dir, "release")

	var stderr strings.Builder
	holder := exec.Command(procbin.Self(t, "hold"), registryDir, path, entered, release)
	holder.Stderr = &stderr
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill() })
	if !appears(entered) {
		t.Fatal("the reader in the other process never handed its read to its store")
	}
	waiter := readerWith(registry, func(follow.ReadResult) follow.Storing {
		t.Error("the store was handed a read of a file a reader in another process holds")
		return follow.Stored
	})
	next := waiter.Advance(path)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("the reader in the other process failed: %v\n%s", err, stderr.String())
	}

	if !next {
		t.Error("Advance on a held file = false, want the project's next entry still read")
	}
	if f := registeredEntry(t, registry, path); f.Offset != int64(len(line)) || f.NextSegment != 1 {
		t.Errorf("cursor = %+v, want it where the other process moved it", f)
	}
}

func registeredEntry(t *testing.T, registry *follow.Registry, path string) follow.File {
	t.Helper()
	files, err := registry.Files(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("registry = %+v, want an entry for %q", files, path)
	return follow.File{}
}
