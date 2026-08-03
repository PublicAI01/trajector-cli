package fsatomic_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

func TestUpdateStartsFromAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	err := fsatomic.Update(path, 0o600, func(old []byte) ([]byte, error) {
		if old != nil {
			t.Errorf("old = %q, want nil for a missing file", old)
		}
		return []byte("first"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "first" {
		t.Errorf("stored = %q, %v", got, err)
	}
}

func TestUpdateSerializesConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "counter")
	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = fsatomic.Update(path, 0o600, func(old []byte) ([]byte, error) {
				count := 0
				if len(old) > 0 {
					var err error
					if count, err = strconv.Atoi(string(old)); err != nil {
						return nil, err
					}
				}
				return []byte(strconv.Itoa(count + 1)), nil
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != strconv.Itoa(n) {
		t.Errorf("counter = %s after %d updates, want %d: an update was lost", got, n, n)
	}
}

func TestUpdateLeavesTheFileAloneWhenFnFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := fsatomic.Update(path, 0o600, func([]byte) ([]byte, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("Update = %v, want the fn error", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "kept" {
		t.Errorf("file = %q after a failed update, want untouched", got)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Error("lock file left behind after a failed update")
	}
}

func TestUpdateEjectsALockWhoseHolderDied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	lock := path + ".lock"
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	longDead := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lock, longDead, longDead); err != nil {
		t.Fatal(err)
	}
	err := fsatomic.Update(path, 0o600, func([]byte) ([]byte, error) { return []byte("recovered"), nil })
	if err != nil {
		t.Fatalf("Update behind a stale lock = %v, want the lock ejected", err)
	}
}
