package consent

// CoversSince is CoversUpdates for an UpdatesSince of since, so the
// comparison is tested on dates other than the one the agreement carries.
func (s *Store) CoversSince(since string) (bool, error) {
	return s.coversSince(since)
}
