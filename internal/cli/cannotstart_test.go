package cli_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/report"
)

// exitsAtOnce is the behavior of a build whose proxy dies on this
// machine's state before it serves.
const exitsAtOnce = "exits-at-once"

// startsDieBeforeServing has every process the CLI starts from here on
// be a real one that exits before it serves.
func startsDieBeforeServing(t *testing.T) {
	t.Helper()
	t.Setenv(cli.RecordProxyStartEnv, "")
	procbin.Self(t, exitsAtOnce)
}

func TestASessionHookSaysFirstThatTheBuildCannotStartItsProxyWhateverElseItHasToSay(t *testing.T) {
	e := clitest.New(t)
	// An agreement accepted before the one this build shows: starting
	// the session pauses recording and says so on stderr.
	e.Sandbox().AcceptAgreement("2020-01-obsolete", "2020-01-01T00:00:00Z")
	e.OnRecordAsUnableToStartItsProxy()
	startsDieBeforeServing(t)

	got := e.InProject("hook", "ensure-proxy")

	if got.Exit == 0 || got.Exit == 2 || got.Stdout != "" {
		t.Errorf("exit = %d, stdout = %q; want a failure that lets the session go on, and silence", got.Exit, got.Stdout)
	}
	lines := strings.Split(strings.TrimSuffix(got.Stderr, "\n"), "\n")
	if want := report.CannotStart("dev"); lines[0] != want {
		t.Fatalf("stderr = %q, want its first line %q", got.Stderr, want)
	}
	if !strings.Contains(strings.Join(lines[1:], "\n"), "agreement changed") {
		t.Errorf("stderr = %q, want the pause explained after the first line", got.Stderr)
	}
}
