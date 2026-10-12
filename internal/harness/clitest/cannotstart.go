package clitest

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

// OnRecordAsUnableToStartItsProxy puts the build this environment runs
// on record as one that cannot start its proxy, as a takeover by it
// that ended without a healthy proxy leaves it.
func (e *Env) OnRecordAsUnableToStartItsProxy() {
	e.t.Helper()
	got := e.Run("version")
	version, ok := strings.CutPrefix(strings.TrimSuffix(got.Stdout, "\n"), "trajector ")
	if got.Exit != 0 || !ok {
		e.t.Fatalf("version = exit %d, stdout %q; want its one line", got.Exit, got.Stdout)
	}
	path := e.Layout().ProxyCannotStart()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := fsatomic.WriteFile(path, []byte(version), 0o600); err != nil {
		e.t.Fatal(err)
	}
}
