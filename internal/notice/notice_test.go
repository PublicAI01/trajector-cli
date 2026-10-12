package notice_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/notice"
)

func storeIn(t *testing.T) (*notice.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "trajector", "notices.json")
	return notice.Open(path), path
}

func take(t *testing.T, s *notice.Store) []string {
	t.Helper()
	lines, err := s.Take()
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	return lines
}

func add(t *testing.T, s *notice.Store, lines ...string) {
	t.Helper()
	if err := s.Add(lines...); err != nil {
		t.Fatalf("Add: %v", err)
	}
}

func TestALineIsShownOnceInTheOrderItWasAdded(t *testing.T) {
	s, _ := storeIn(t)
	add(t, s, "first.", "second.")
	add(t, s, "third.")

	if got, want := take(t, s), []string{"first.", "second.", "third."}; !slices.Equal(got, want) {
		t.Errorf("Take = %q, want %q", got, want)
	}
	if got := take(t, s); len(got) != 0 {
		t.Errorf("second Take = %q, want nothing: a line is said once", got)
	}
}

func TestNothingWaitingCreatesNoFile(t *testing.T) {
	s, path := storeIn(t)
	if got := take(t, s); len(got) != 0 {
		t.Errorf("Take = %q, want nothing", got)
	}
	add(t, s)
	add(t, s, "")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Stat(%s) = %v, want no file for a store nothing was added to", path, err)
	}
}

func TestALineAlreadyWaitingIsNotAddedTwice(t *testing.T) {
	s, _ := storeIn(t)
	add(t, s, "upgraded.", "paused.")
	add(t, s, "paused.", "", "repaired.", "repaired.")

	if got, want := take(t, s), []string{"upgraded.", "paused.", "repaired."}; !slices.Equal(got, want) {
		t.Errorf("Take = %q, want %q", got, want)
	}
	// Once taken, the same line is news again.
	add(t, s, "paused.")
	if got, want := take(t, s), []string{"paused."}; !slices.Equal(got, want) {
		t.Errorf("Take = %q, want %q", got, want)
	}
}

func TestOnlyTheNewestLinesWait(t *testing.T) {
	s, _ := storeIn(t)
	var all []string
	for i := range notice.MaxLines + 5 {
		line := fmt.Sprintf("line %d.", i)
		all = append(all, line)
		add(t, s, line)
	}

	if got, want := take(t, s), all[5:]; !slices.Equal(got, want) {
		t.Errorf("Take = %q, want the newest %d: %q", got, notice.MaxLines, want)
	}
}

func TestLinesAddedAtOnceAreAllKept(t *testing.T) {
	s, _ := storeIn(t)
	var wg sync.WaitGroup
	want := make([]string, 0, notice.MaxLines)
	for i := range notice.MaxLines {
		line := fmt.Sprintf("from writer %d.", i)
		want = append(want, line)
		wg.Go(func() {
			if err := s.Add(line); err != nil {
				t.Errorf("Add(%q): %v", line, err)
			}
		})
	}
	wg.Wait()

	got := take(t, s)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("Take = %q, want every line a writer added: %q", got, want)
	}
}

func TestAFileThatDoesNotParseHoldsNothingAndIsReplaced(t *testing.T) {
	s, path := storeIn(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{cut"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := take(t, s); len(got) != 0 {
		t.Errorf("Take = %q, want nothing from a file that does not parse", got)
	}
	add(t, s, "after.")
	if got, want := take(t, s), []string{"after."}; !slices.Equal(got, want) {
		t.Errorf("Take = %q, want %q", got, want)
	}
}

func TestTheFileIsPrivateToTheUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no unix permission bits")
	}
	s, path := storeIn(t)
	add(t, s, "upgraded.")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if mode := dir.Mode().Perm(); mode != 0o700 {
		t.Errorf("directory mode = %o, want 700", mode)
	}
}

func TestAStoreItCannotReachSaysSo(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := notice.Open(filepath.Join(blocker, "notices.json"))
	if err := s.Add("upgraded."); err == nil {
		t.Error("Add under a regular file succeeded")
	}

	// A directory where the file should be cannot be read as one.
	dir := t.TempDir()
	if _, err := notice.Open(dir).Take(); err == nil {
		t.Error("Take of a directory succeeded")
	}
}

func TestATakeThatCannotEmptyTheFileReturnsNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no unix permission bits")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	s, path := storeIn(t)
	add(t, s, "upgraded.")
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	// A line that cannot be removed is not handed out: shown now and
	// still waiting, it would be shown again by every command.
	lines, err := s.Take()
	if err == nil || len(lines) != 0 {
		t.Errorf("Take = %q, %v; want an error and no lines", lines, err)
	}
}
