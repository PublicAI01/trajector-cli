//go:build windows

package fsatomic

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Windows fails an open that crosses a replacing rename with a sharing
// violation, and MoveFileEx refuses to replace a destination that any
// handle holds open. ReadFile and WriteFile together must surface
// neither: hammering them against each other stays error-free, where
// os.ReadFile against an os.Rename-based WriteFile goes red within a
// second on both sides.
func TestReadAndWriteFileTolerateEachOthersReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "table.json")
	if err := WriteFile(path, []byte(`{"n":0}`), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var wg sync.WaitGroup

	wg.Go(func() {
		for time.Now().Before(deadline) {
			if err := WriteFile(path, []byte(`{"n":1}`), 0o600); err != nil {
				t.Errorf("write: %v", err)
			}
		}
	})

	for range 8 {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				if _, err := ReadFile(path); err != nil && !os.IsNotExist(err) {
					t.Errorf("read: %v", err)
				}
			}
		})
	}
	wg.Wait()
}

// holdOpen opens path the way os.Open does on Windows, without delete
// sharing, which makes every rename of path fail until the handle is
// closed after hold.
func holdOpen(t *testing.T, path string, hold time.Duration) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	released := time.AfterFunc(hold, func() { f.Close() })
	t.Cleanup(func() {
		if released.Stop() {
			f.Close()
		}
	})
}

func TestRenameWaitsForAHolderThatLetsGoWithinTheWindow(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	if err := os.WriteFile(from, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	holdOpen(t, from, 300*time.Millisecond)

	if err := Rename(from, to, 5*time.Second); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if data, err := os.ReadFile(to); err != nil || string(data) != "content" {
		t.Errorf("the renamed file holds %q, %v; want the content", data, err)
	}
}

func TestRenameFailsWhenTheHolderOutlastsTheWindow(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	if err := os.WriteFile(from, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	holdOpen(t, from, time.Minute)

	if err := Rename(from, to, 300*time.Millisecond); err == nil {
		t.Fatal("Rename succeeded while the file was held open")
	}
	if data, err := os.ReadFile(from); err != nil || string(data) != "content" {
		t.Errorf("the file holds %q, %v; want it unmoved", data, err)
	}
}
