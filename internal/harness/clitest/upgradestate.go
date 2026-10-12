package clitest

import (
	"time"

	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// UpgradeState is when the background upgrade check last ran on this
// environment's device and what it came to, as the state file records
// them: the zero time and an empty result when it never ran.
func (e *Env) UpgradeState() (checkedAt time.Time, result string) {
	e.t.Helper()
	s, err := selfupdate.LoadState(e.Layout().UpgradeState())
	if err != nil {
		e.t.Fatal(err)
	}
	return s.CheckedAt, string(s.Result)
}
