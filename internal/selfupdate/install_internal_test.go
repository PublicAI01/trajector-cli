package selfupdate

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestInstallStagesThroughTheFlushingWriter pins the 2026-09-16 fix.
// install used to write the staged binary with a plain Write/Close and
// then rename it over execPath, flushing neither. A rename only orders
// a directory entry, so a crash inside the writeback window left
// execPath naming blocks that were never written while that rename had
// already unlinked the previous binary: the file reads back empty with
// mode 0755 on it and there is no trajector left to retry with.
// fsatomic is where the flush lives, so the assertion is that the
// staging goes through it rather than around it.
func TestInstallStagesThroughTheFlushingWriter(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "trajector")
	if err := os.WriteFile(execPath, []byte("previous"), 0o755); err != nil {
		t.Fatalf("seeding the previous binary: %v", err)
	}

	type staging struct {
		path string
		data []byte
		perm fs.FileMode
	}
	var staged []staging
	previous := stageBinary
	stageBinary = func(path string, data []byte, perm fs.FileMode) error {
		staged = append(staged, staging{path, data, perm})
		return previous(path, data, perm)
	}
	t.Cleanup(func() { stageBinary = previous })

	replacement := []byte("the new binary")
	if err := install(execPath, replacement); err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("staged through the flushing writer %d time(s), want 1: a binary nobody flushed can read back empty after a crash, and the rename has already taken the previous one", len(staged))
	}
	if got := staged[0].path; got == execPath || filepath.Dir(got) != dir {
		t.Errorf("staged at %q, want a sibling of %q: the install must not be written over the running image", got, execPath)
	}
	if string(staged[0].data) != string(replacement) {
		t.Errorf("staged %q, want the downloaded binary %q", staged[0].data, replacement)
	}
	if staged[0].perm != executablePerm {
		t.Errorf("staged with mode %v, want %v so the install is runnable whatever umask the upgrade ran under", staged[0].perm, executablePerm)
	}
}
