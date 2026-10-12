package lifecycle

import (
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/consent"
)

// CoverUpdatesSince has the gate on the agreement read this device's
// acceptance as if the agreement had carried its Updates section since
// the release day since, until the test ends: an accepted version on or
// after it covers the automatic check. No agreement carries the section
// yet, so without it no device would pass the gate.
func CoverUpdatesSince(t *testing.T, since string) {
	t.Helper()
	day, err := time.Parse(time.DateOnly, since)
	if err != nil {
		t.Fatal(err)
	}
	previous := updatesCovered
	updatesCovered = func(s *consent.Store) (bool, error) {
		accepted, _, err := s.AcceptedVersion()
		if err != nil {
			return false, err
		}
		on, err := time.Parse(time.DateOnly, accepted)
		return err == nil && !on.Before(day), nil
	}
	t.Cleanup(func() { updatesCovered = previous })
}
