package proxyserve

import (
	"testing"
	"time"
)

// SetSweepInterval makes every proxy this test serves sweep its hot
// session files every interval, until t ends.
func SetSweepInterval(t testing.TB, interval time.Duration) {
	t.Helper()
	was := sweepInterval
	sweepInterval = interval
	t.Cleanup(func() { sweepInterval = was })
}
