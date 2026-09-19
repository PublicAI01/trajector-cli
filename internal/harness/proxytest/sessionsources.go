package proxytest

import (
	"runtime"
	"testing"
)

// sessionSourcesSkip is the one reason a test states when it stands
// down. Packages below this harness cannot import it, so their own
// skips repeat this sentence and name the guard beside it.
const sessionSourcesSkip = "the session-file and git sources do not work on native Windows, which is no release target"

// RequireSessionSources stands a test down on native Windows, where
// neither the session-file source nor the git source works: short path
// names, absent inodes, and permission bits that carry nothing all
// change what these tests observe. The proxy path is unaffected and is
// never skipped here.
func RequireSessionSources(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip(sessionSourcesSkip)
	}
}
