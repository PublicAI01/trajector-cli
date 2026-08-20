package fsatomic

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swapSyncFile installs f as the flush this package performs before a
// rename, for the duration of one test.
func swapSyncFile(t *testing.T, f func(*os.File) error) {
	t.Helper()
	previous := syncFile
	syncFile = f
	t.Cleanup(func() { syncFile = previous })
}

// TestWriteFileFlushesContentBeforeTheRename pins the durability half of
// this package's promise. Renaming without flushing first only orders
// the directory entry: a crash between the two leaves the entry pointing
// at unwritten blocks, so the file reads back empty while the content it
// replaced is already gone.
func TestWriteFileFlushesContentBeforeTheRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	var flushed []string
	swapSyncFile(t, func(f *os.File) error {
		flushed = append(flushed, filepath.Base(f.Name()))
		return f.Sync()
	})

	if err := WriteFile(path, []byte(`{"env":{}}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if len(flushed) != 1 {
		t.Fatalf("flushed %d file(s), want 1: a rename that outlives its data leaves an empty file where the previous content was", len(flushed))
	}
	if want := filepath.Base(path) + tempMarker; !strings.HasPrefix(flushed[0], want) {
		t.Errorf("flushed %q, want a temp sibling named %q...", flushed[0], want)
	}
}

// TestWriteFileKeepsThePreviousContentWhenTheFlushFails pins the other
// half: content that could not reach stable storage must not be renamed
// over content that already did.
func TestWriteFileKeepsThePreviousContentWhenTheFlushFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	full := errors.New("no space left on device")
	swapSyncFile(t, func(*os.File) error { return full })

	if err := WriteFile(path, []byte("replacement"), 0o600); !errors.Is(err, full) {
		t.Fatalf("WriteFile = %v, want the flush failure", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "previous" {
		t.Errorf("file = %q, %v, want %q, nil", got, err, "previous")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the target: the unflushed temp file was left behind", len(entries))
	}
}
