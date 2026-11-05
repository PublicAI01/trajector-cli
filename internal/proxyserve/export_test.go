package proxyserve

import (
	"testing"
	"time"
)

// ExitFlushBudget is what a served proxy is budgeted for on its way out.
const ExitFlushBudget = exitFlushBudget

// SetSweepInterval makes every proxy this test serves sweep its hot
// session files every interval, until t ends.
func SetSweepInterval(t testing.TB, interval time.Duration) {
	t.Helper()
	was := sweepInterval
	sweepInterval = interval
	t.Cleanup(func() { sweepInterval = was })
}
