//go:build !linux

package follow_test

import "testing"

// registryWrites counts the writes that replace the file at path, and
// reports whether it could count them. Only Linux reports them to a
// test, through inotify: elsewhere nothing is counted, and the test
// checks the rest of what it is for.
func registryWrites(t *testing.T, _ string) func() (n int, counted bool) {
	t.Helper()
	t.Log("the writes of a file are not counted here: that needs inotify, which only Linux has")
	return func() (int, bool) { return 0, false }
}
