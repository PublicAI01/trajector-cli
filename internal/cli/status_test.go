package cli_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
)

func TestStatusRunsOnAFreshDevice(t *testing.T) {
	e := clitest.New(t)
	got := e.InProject("status")
	if got.Exit != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %q)", got.Exit, got.Stderr)
	}
	for _, want := range []string{"Not signed in", "Not running"} {
		if !strings.Contains(got.Stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", got.Stdout, want)
		}
	}
}

func TestStatusRejectsStrayArguments(t *testing.T) {
	e := clitest.New(t)
	got := e.Run("status", "extra")
	if got.Exit != 2 || !strings.Contains(got.Stderr, "usage: trajector status") {
		t.Errorf("got %+v, want a usage error", got)
	}
}
