package cli_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
)

func TestDoctorRequeueUsage(t *testing.T) {
	e := clitest.New(t)
	for _, args := range [][]string{
		{"doctor", "requeue"},
		{"doctor", "requeue", "b-1", "b-2"},
		{"doctor", "requeue", "--all", "b-1"},
	} {
		got := e.Run(args...)
		if got.Exit != 2 || !strings.Contains(got.Stderr, "usage: trajector doctor requeue") {
			t.Errorf("%v -> %+v, want a usage error", args, got)
		}
	}
}

func TestDoctorRequeueAllOnAnEmptyStore(t *testing.T) {
	e := clitest.New(t)
	got := e.Run("doctor", "requeue", "--all")
	if got.Exit != 0 || !strings.Contains(got.Stdout, "No rejected batches") {
		t.Errorf("got %+v, want a clean nothing-to-do report", got)
	}
}
