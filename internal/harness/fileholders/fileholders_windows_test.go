//go:build windows

package fileholders_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fileholders"
)

func TestOfNamesTheProcessThatHoldsTheFileOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	holders, err := fileholders.Of(path)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	if !slices.ContainsFunc(holders, func(h fileholders.Holder) bool { return h.PID == uint32(os.Getpid()) }) {
		t.Errorf("Of = %v, want this test's process, pid %d", holders, os.Getpid())
	}
}
