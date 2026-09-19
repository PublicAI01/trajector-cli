package gitsnapshot_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/gitsnapshot"
)

const (
	hashA   = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	commitA = "1111111111111111111111111111111111111111"
	commitB = "2222222222222222222222222222222222222222"
	commitC = "abcdef0123456789abcdef0123456789abcdef01"
)

func newHeads(t *testing.T) (*gitsnapshot.Heads, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "git-heads.json")
	return gitsnapshot.OpenHeads(path), path
}

func TestHeadsAreEmptyUntilACommitIsSeen(t *testing.T) {
	h, path := newHeads(t)
	if got, ok := h.Last(hashA); ok {
		t.Errorf("Last = %q before anything was seen", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("reading created the file (stat: %v)", err)
	}
}

func TestHeadsRememberTheLastCommitPerProject(t *testing.T) {
	h, _ := newHeads(t)
	const hashB = "b1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	if err := h.See(hashA, commitA); err != nil {
		t.Fatal(err)
	}
	if err := h.See(hashB, commitB); err != nil {
		t.Fatal(err)
	}
	if err := h.See(hashA, commitB); err != nil {
		t.Fatal(err)
	}
	got, ok := h.Last(hashA)
	if !ok || got != commitB {
		t.Errorf("Last(A) = %q/%v, want the commit seen last", got, ok)
	}
	if got, ok := h.Last(hashB); !ok || got != commitB {
		t.Errorf("Last(B) = %q/%v, want its own commit untouched", got, ok)
	}
}

func TestHeadsAreReadableOnlyByTheirOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the session-file and git sources do not work on native Windows, which is no release target (proxytest.RequireSessionSources)")
	}
	h, path := newHeads(t)
	if err := h.See(hashA, commitA); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
}

func TestHeadsRefuseWhatCouldNotBeHandedBackToGit(t *testing.T) {
	h, _ := newHeads(t)
	tests := []struct {
		name          string
		projectIDHash string
		head          string
	}{
		{name: "no project", head: commitA},
		{name: "an abbreviated commit", projectIDHash: hashA, head: commitA[:7]},
		{name: "a commit with an upper-case digit", projectIDHash: hashA, head: strings.ToUpper(commitC)},
		{name: "a value shaped like an option", projectIDHash: hashA, head: "--output=x"},
		{name: "nothing at all", projectIDHash: hashA},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := h.See(tt.projectIDHash, tt.head); err == nil {
				t.Error("the store kept a value it could not hand back to git")
			}
		})
	}
}

func TestHeadsForgetOneProjectAndLeaveTheRest(t *testing.T) {
	h, _ := newHeads(t)
	const hashB = "b1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	if err := h.See(hashA, commitA); err != nil {
		t.Fatal(err)
	}
	if err := h.See(hashB, commitB); err != nil {
		t.Fatal(err)
	}
	if err := h.Forget(hashA); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Last(hashA); ok {
		t.Error("a forgotten project is still remembered")
	}
	if got, ok := h.Last(hashB); !ok || got != commitB {
		t.Errorf("Last(B) = %q/%v, want it untouched", got, ok)
	}
}

func TestForgettingWhatWasNeverRememberedIsNothingToDo(t *testing.T) {
	h, path := newHeads(t)
	if err := h.Forget(hashA); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("forgetting created the file (stat: %v)", err)
	}
}

func TestHeadsRefuseAFileOfAnotherLayout(t *testing.T) {
	h, path := newHeads(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":99,"heads":{"`+hashA+`":"`+commitA+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := h.Last(hashA); ok {
		t.Errorf("Last = %q from a layout this package does not read", got)
	}
	if err := h.See(hashA, commitB); err == nil {
		t.Error("a file of another layout was written over")
	}
}

func TestAnUnreadableCommitIsTheSameAsNoneRemembered(t *testing.T) {
	h, path := newHeads(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"heads":{"`+hashA+`":"HEAD"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := h.Last(hashA); ok {
		t.Errorf("Last = %q, want a value that is not a commit ignored", got)
	}
}
