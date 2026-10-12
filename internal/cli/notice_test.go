package cli_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
)

const upgradedLine = "trajector upgraded itself to 9.9.9 in the background."

func TestACommandThePersonRunsSaysWhatWasLeftForItOnce(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"forget", "sess-1"}, {"doctor", "requeue", "--all"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e := clitest.New(t)
			agreementLine := "trajector: the data agreement changed; run `trajector enable`"
			e.LeaveNotice(upgradedLine, agreementLine)

			got := e.InProject(args...)
			if want := upgradedLine + "\n" + agreementLine + "\n"; !strings.HasPrefix(got.Stderr, want) {
				t.Errorf("stderr = %q, want it to open with %q", got.Stderr, want)
			}
			// The lines are hints on stderr: what the command prints on
			// stdout is still its own answer alone.
			if strings.Contains(got.Stdout, upgradedLine) {
				t.Errorf("stdout = %q, want the line on stderr only", got.Stdout)
			}
			if again := e.InProject(args...); strings.Contains(again.Stderr, upgradedLine) {
				t.Errorf("second run stderr = %q, want the line said once", again.Stderr)
			}
		})
	}
}

func TestACommandThatRefusesToStartLeavesTheLinesForTheNextOne(t *testing.T) {
	e := clitest.New(t)
	e.LeaveNotice(upgradedLine)
	e.WriteConfig("{not json")

	if got := e.Run("upload"); got.Exit == 0 {
		t.Fatalf("upload ran on a config file it cannot read: %+v", got)
	}
	if got := e.WaitingNotices(); !slices.Equal(got, []string{upgradedLine}) {
		t.Errorf("waiting = %q, want the line kept", got)
	}
}

func TestWhatOnlyAProgramReadsLeavesTheLinesWaiting(t *testing.T) {
	cases := map[string]func(e *clitest.Env) clitest.Result{
		"version":     func(e *clitest.Env) clitest.Result { return e.Run("version") },
		"--version":   func(e *clitest.Env) clitest.Result { return e.Run("--version") },
		"--help":      func(e *clitest.Env) clitest.Result { return e.Run("--help") },
		"status -h":   func(e *clitest.Env) clitest.Result { return e.Run("status", "-h") },
		"hook":        func(e *clitest.Env) clitest.Result { return e.InProject("hook", "session-end") },
		"hook read":   func(e *clitest.Env) clitest.Result { return e.Run("hook", "read", e.ProjectRoot()) },
		"hook finish": func(e *clitest.Env) clitest.Result { return e.Run("hook", "upgrade-finish") },
		"proxy": func(e *clitest.Env) clitest.Result {
			e.StartProxy().Stop()
			return clitest.Result{}
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			e := clitest.New(t)
			e.LeaveNotice(upgradedLine)

			got := run(e)
			if strings.Contains(got.Stdout+got.Stderr, upgradedLine) {
				t.Errorf("output = %q %q, want the line left for a person", got.Stdout, got.Stderr)
			}
			if lines := e.WaitingNotices(); !slices.Contains(lines, upgradedLine) {
				t.Errorf("waiting = %q, want %q still there", lines, upgradedLine)
			}
		})
	}
}

func TestVersionPrintsOnlyItsAnswerWhateverWaits(t *testing.T) {
	e := clitest.New(t)
	e.LeaveNotice(upgradedLine)

	// A newly downloaded build is run as `version` before it is
	// installed, and its answer is compared whole.
	got := e.Run("version")
	if got.Stdout != "trajector dev\n" || got.Stderr != "" {
		t.Errorf("version printed %q / %q, want only its own line", got.Stdout, got.Stderr)
	}
}

func TestStatusOfABuildFromSourceSaysItNeverChecks(t *testing.T) {
	e := clitest.New(t)
	// What a release build of this device last found does not make
	// this one, built from source, check on its own.
	e.SeedUpgradeState(time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC), "newest")

	got := e.InProject("status")
	if want := "Automatic upgrades: off (not a release build)."; !strings.Contains(got.Stdout, want) {
		t.Errorf("status = %s\nwant it to contain %q", got.Stdout, want)
	}
}
