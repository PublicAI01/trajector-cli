// Package procbin re-executes the test binary as an arbitrary program
// so process-level tests (crash, restart, takeover) can drive real
// processes without building the CLI first. A test registers named
// behaviors in TestMain; Self marks the environment so that the next
// execution of the test binary — and every process it spawns in turn —
// runs the chosen behavior instead of the test suite.
package procbin

import (
	"fmt"
	"os"
	"testing"
)

const envKey = "TRAJECTOR_PROCBIN_BEHAVIOR"

// Main must be called from TestMain. In a re-executed child it runs
// the requested behavior with the process arguments and exits; in the
// test process it runs the tests.
func Main(m *testing.M, behaviors map[string]func(args []string) int) {
	if name := os.Getenv(envKey); name != "" {
		behavior, ok := behaviors[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "procbin: unknown behavior %q\n", name)
			os.Exit(97)
		}
		os.Exit(behavior(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// Self selects the behavior for every process the test spawns from
// here on (directly or transitively) and returns the binary to spawn.
func Self(t *testing.T, behavior string) string {
	t.Helper()
	t.Setenv(envKey, behavior)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("procbin: locating test binary: %v", err)
	}
	return exe
}
