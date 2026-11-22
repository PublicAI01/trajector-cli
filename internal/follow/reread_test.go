package follow_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
)

// keptOnDisk keeps segments the way the spool does, in a directory two
// processes can share: a segment index that is stored already keeps the
// lines it was stored with, and a second write under it reports success
// and changes nothing.
type keptOnDisk struct{ dir string }

func (k keptOnDisk) store(res follow.ReadResult) follow.Storing {
	for _, seg := range res.Segments {
		name := filepath.Join(k.dir, fmt.Sprintf("%06d", seg.SegmentIndex))
		fh, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return follow.NotStored
		}
		_, werr := fh.WriteString(seg.Lines)
		if cerr := fh.Close(); werr != nil || cerr != nil {
			return follow.NotStored
		}
	}
	return follow.Stored
}

// lines is every line of every stored segment, in segment order.
func (k keptOnDisk) lines(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(k.dir)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(k.dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for l := range strings.SplitAfterSeq(string(data), "\n") {
			if l != "" {
				lines = append(lines, l)
			}
		}
	}
	return lines
}

// dieOnceStored reads the entry registered for args[1] in the registry
// at args[0], stores what it read under args[2], and is killed inside
// the store: the segment is stored and the cursor is never written.
func dieOnceStored(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: die-once-stored <registry-dir> <path> <store-dir>")
		return 2
	}
	kept := keptOnDisk{dir: args[2]}
	reader := readerWith(follow.Open(args[0]), func(res follow.ReadResult) follow.Storing {
		if kept.store(res) != follow.Stored {
			return follow.NotStored
		}
		self, err := os.FindProcess(os.Getpid())
		if err != nil || self.Kill() != nil {
			return follow.NotStored
		}
		select {}
	})
	reader.Advance(args[1])
	return 3
}

// expireEntryLocks ages every lock under registryDir past the time a
// live reader may hold one, as the lock of a reader that died is once
// that time has passed.
func expireEntryLocks(t *testing.T, registryDir string) {
	t.Helper()
	locks, err := filepath.Glob(filepath.Join(registryDir, "*.lock"))
	if err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-time.Hour)
	for _, lock := range locks {
		if err := os.Chtimes(lock, long, long); err != nil {
			t.Fatal(err)
		}
	}
}

func sixLines() []string {
	return []string{userLine(1), userLine(2), userLine(3), userLine(4), userLine(5), userLine(6)}
}

// readTwiceMore appends the fourth and fifth line and reads, then the
// sixth and reads, as two later runs do.
func readTwiceMore(t *testing.T, registry *follow.Registry, path string, lines []string, kept keptOnDisk) {
	t.Helper()
	reader := readerWith(registry, kept.store)
	appendFile(t, path, lines[3]+lines[4])
	reader.Advance(path)
	appendFile(t, path, lines[5])
	reader.Advance(path)
}

// readWithTheCursorUnwritable reads path once with the registry file
// out of reach while the cursor is written: the read is stored and the
// cursor stays where it was.
func readWithTheCursorUnwritable(t *testing.T, registry *follow.Registry, registryDir, path string, kept keptOnDisk) {
	t.Helper()
	registryFile := filepath.Join(registryDir, project+".json")
	away := registryFile + ".away"
	unwritable := readerWith(registry, func(res follow.ReadResult) follow.Storing {
		answer := kept.store(res)
		if err := os.Rename(registryFile, away); err != nil {
			t.Error(err)
		}
		return answer
	})
	unwritable.Advance(path)
	if err := os.Rename(away, registryFile); err != nil {
		t.Fatal(err)
	}
	if f := registeredEntry(t, registry, path); f.Offset != 0 || f.NextSegment != 0 {
		t.Fatalf("cursor = %+v, want it left where it was: this test needs the cursor write to fail", f)
	}
}

func TestReader_LinesReadAgainAfterTheCursorCouldNotBeWrittenAreStored(t *testing.T) {
	registryDir := filepath.Join(t.TempDir(), "follow")
	registry := follow.Open(registryDir)
	path := mainPath(t)
	lines := sixLines()
	writeFile(t, path, strings.Join(lines[:3], ""))
	mustRegister(t, registry, project, path)
	kept := keptOnDisk{dir: t.TempDir()}

	readWithTheCursorUnwritable(t, registry, registryDir, path, kept)
	readTwiceMore(t, registry, path, lines, kept)

	if got := kept.lines(t); !slices.Equal(got, lines) {
		t.Errorf("stored lines = %q\nwant every line once, in file order: %q", got, lines)
	}
	if f := registeredEntry(t, registry, path); f.Offset != int64(len(strings.Join(lines, ""))) {
		t.Errorf("cursor = %+v, want it at the end of the file", f)
	}
}

func TestReader_AFileRewrittenAfterTheCursorCouldNotBeWrittenIsStoredUnderTheNextIndex(t *testing.T) {
	registryDir := filepath.Join(t.TempDir(), "follow")
	registry := follow.Open(registryDir)
	path := mainPath(t)
	lines := sixLines()
	writeFile(t, path, strings.Join(lines[:3], ""))
	mustRegister(t, registry, project, path)
	kept := keptOnDisk{dir: t.TempDir()}

	readWithTheCursorUnwritable(t, registry, registryDir, path, kept)
	replaceFile(t, path, strings.Join(lines[3:], ""))
	readerWith(registry, kept.store).Advance(path)

	if got := kept.lines(t); !slices.Equal(got, lines) {
		t.Errorf("stored lines = %q\nwant the lines stored before the rewrite, then the rewritten file: %q", got, lines)
	}
	if f := registeredEntry(t, registry, path); f.NextSegment != 2 {
		t.Errorf("cursor = %+v, want the rewritten file stored under index 1, after the segment the store held", f)
	}
}

func TestRegistry_UnregisterLeavesNothingOfAReadWhoseCursorCouldNotBeWritten(t *testing.T) {
	registryDir := filepath.Join(t.TempDir(), "follow")
	registry := follow.Open(registryDir)
	path := mainPath(t)
	writeFile(t, path, userLine(1))
	mustRegister(t, registry, project, path)
	readWithTheCursorUnwritable(t, registry, registryDir, path, keptOnDisk{dir: t.TempDir()})

	if err := registry.Unregister(project); err != nil {
		t.Fatal(err)
	}

	left, err := os.ReadDir(registryDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range left {
		t.Errorf("%s is still in the registry directory, want nothing of an unregistered project", e.Name())
	}
}

func TestReader_LinesReadAgainAfterAReaderDiedBeforeWritingTheCursorAreStored(t *testing.T) {
	registryDir := filepath.Join(t.TempDir(), "follow")
	registry := follow.Open(registryDir)
	path := mainPath(t)
	lines := sixLines()
	writeFile(t, path, strings.Join(lines[:3], ""))
	mustRegister(t, registry, project, path)
	kept := keptOnDisk{dir: t.TempDir()}

	var stderr strings.Builder
	dying := exec.CommandContext(t.Context(), procbin.Self(t, "die-once-stored"), registryDir, path, kept.dir)
	dying.Stderr = &stderr
	err := dying.Run()
	if got := kept.lines(t); !slices.Equal(got, lines[:3]) {
		t.Fatalf("lines stored by the reader that is killed once it stored them = %q, want %q (%v)\n%s", got, lines[:3], err, stderr.String())
	}
	if f := registeredEntry(t, registry, path); f.Offset != 0 || f.NextSegment != 0 {
		t.Fatalf("cursor = %+v, want it left where it was: the reader died before writing it", f)
	}
	expireEntryLocks(t, registryDir)

	readTwiceMore(t, registry, path, lines, kept)

	if got := kept.lines(t); !slices.Equal(got, lines) {
		t.Errorf("stored lines = %q\nwant every line once, in file order: %q", got, lines)
	}
}
