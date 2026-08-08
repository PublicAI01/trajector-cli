package fsatomic_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
)

func TestMain(m *testing.M) {
	procbin.Main(m, map[string]func(args []string) int{
		"writer": rewriteUntilDeadline,
	})
}

const writerPayloadSize = 4096

// rewriteUntilDeadline rewrites args[0] with copies of args[1] until
// the unix-millisecond deadline in args[2] passes, then prints how many
// writes landed. Any write error is fatal to this process: the test
// spawning it asserts that concurrent writers never fail each other.
func rewriteUntilDeadline(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: writer <path> <fill> <deadline-ms>")
		return 2
	}
	path, fill := args[0], args[1]
	ms, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	payload := bytes.Repeat([]byte(fill), writerPayloadSize)
	writes := 0
	for time.Now().Before(time.UnixMilli(ms)) {
		if err := fsatomic.WriteFile(path, payload, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		writes++
	}
	fmt.Println(writes)
	return 0
}

func TestTwoProcessesRewritingOnePathNeverTearItOrFailEachOther(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	exe := procbin.Self(t, "writer")
	deadline := time.Now().Add(2 * time.Second)
	deadlineArg := strconv.FormatInt(deadline.UnixMilli(), 10)

	fills := []string{"a", "b"}
	writers := make([]*exec.Cmd, len(fills))
	var stdout, stderr [2]bytes.Buffer
	for i, fill := range fills {
		cmd := exec.Command(exe, path, fill, deadlineArg)
		cmd.Stdout = &stdout[i]
		cmd.Stderr = &stderr[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		writers[i] = cmd
	}

	reads := 0
	for time.Now().Before(deadline) {
		data, err := fsatomic.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read during concurrent writes: %v", err)
		}
		if len(data) != writerPayloadSize {
			t.Fatalf("torn read: %d bytes, want %d", len(data), writerPayloadSize)
		}
		if data[0] != 'a' && data[0] != 'b' {
			t.Fatalf("read content from no writer: starts with %q", data[0])
		}
		if trailing := bytes.TrimLeft(data, string(data[0])); len(trailing) != 0 {
			t.Fatalf("read mixes two writers' content: %q then %q", data[0], trailing[0])
		}
		reads++
	}
	if reads == 0 {
		t.Error("the reader never observed a written file")
	}

	for i, cmd := range writers {
		if err := cmd.Wait(); err != nil {
			t.Errorf("writer %q failed: %v\n%s", fills[i], err, stderr[i].String())
			continue
		}
		writes, err := strconv.Atoi(strings.TrimSpace(stdout[i].String()))
		if err != nil || writes == 0 {
			t.Errorf("writer %q reported %q completed writes", fills[i], stdout[i].String())
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "state.json" {
			t.Errorf("leftover next to the target: %s", e.Name())
		}
	}
}

func TestWriteFileLeavesOnlyTheTargetBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for range 3 {
		if err := fsatomic.WriteFile(path, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory holds %v, want only state.json", names)
	}
}

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
	if err := runCountingUpdates(path, 20); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "20" {
		t.Errorf("counter = %s after 20 updates, want 20: an update was lost", got)
	}
}

func TestConcurrentUpdatesBehindAStaleLockLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "counter")
	lock := path + ".lock"
	if err := os.WriteFile(lock, []byte("dead-holder"), 0o600); err != nil {
		t.Fatal(err)
	}
	longDead := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lock, longDead, longDead); err != nil {
		t.Fatal(err)
	}
	if err := runCountingUpdates(path, 20); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "20" {
		t.Errorf("counter = %s after 20 updates behind a stale lock, want 20", got)
	}
}

func runCountingUpdates(path string, n int) error {
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
	return errors.Join(errs...)
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

func TestAnOutlivedHolderReleaseSparesTheNextHoldersLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	lock := path + ".lock"

	firstEntered := make(chan struct{})
	firstRelease := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- fsatomic.Update(path, 0o600, func([]byte) ([]byte, error) {
			close(firstEntered)
			<-firstRelease
			return []byte("stalled"), nil
		})
	}()
	<-firstEntered
	longDead := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lock, longDead, longDead); err != nil {
		t.Fatal(err)
	}

	secondEntered := make(chan struct{})
	secondRelease := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- fsatomic.Update(path, 0o600, func([]byte) ([]byte, error) {
			close(secondEntered)
			<-secondRelease
			return []byte("second"), nil
		})
	}()
	<-secondEntered

	close(firstRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("stalled update = %v", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("the next holder's lock is gone after the outlived holder released: %v", err)
	}

	close(secondRelease)
	if err := <-secondDone; err != nil {
		t.Fatalf("second update = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "second" {
		t.Errorf("final content = %q, %v, want the second holder's write", got, err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Error("lock file left behind after both updates finished")
	}
}

func TestUpdateSweepsAbandonedTempAndClaimFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	abandoned := []string{
		"state.json.tmp-1234",
		"state.json.lock.eject",
	}
	longDead := time.Now().Add(-time.Minute)
	for _, name := range abandoned {
		leftover := filepath.Join(dir, name)
		if err := os.WriteFile(leftover, []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(leftover, longDead, longDead); err != nil {
			t.Fatal(err)
		}
	}
	fresh := filepath.Join(dir, "state.json.tmp-5678")
	if err := os.WriteFile(fresh, []byte("in flight"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := fsatomic.Update(path, 0o600, func([]byte) ([]byte, error) { return []byte("current"), nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range abandoned {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived an update, want it swept", name)
		}
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a fresh temp file was swept: %v", err)
	}
}
