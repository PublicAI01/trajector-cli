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

	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			if err := WriteFile(path, []byte(`{"n":1}`), 0o600); err != nil {
				t.Errorf("write: %v", err)
			}
		}
	}()

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				if _, err := ReadFile(path); err != nil && !os.IsNotExist(err) {
					t.Errorf("read: %v", err)
				}
			}
		}()
	}
	wg.Wait()
}
