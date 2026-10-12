package clitest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// SeedUpgradeState records a background upgrade check that ran at
// checkedAt and came to result, as a build of this device left it.
func (e *Env) SeedUpgradeState(checkedAt time.Time, result string) {
	e.t.Helper()
	data, err := json.Marshal(selfupdate.State{CheckedAt: checkedAt, Result: selfupdate.Result(result)})
	if err != nil {
		e.t.Fatal(err)
	}
	path := e.Layout().UpgradeState()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := fsatomic.WriteFile(path, data, 0o600); err != nil {
		e.t.Fatal(err)
	}
}
