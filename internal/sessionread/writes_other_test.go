//go:build !linux

package sessionread

import "testing"

// registryWrites counts the writes that replace the file at path. Only
// Linux reports them to a test, through inotify.
func registryWrites(t *testing.T, _ string) func() int {
	t.Helper()
	t.Skip("counting the writes of a file needs inotify, which only Linux has")
	return nil
}
