package cli_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
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

func TestStatusCountsEveryKindOfRecordWaitingInTheSpool(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	at := time.Now().UTC()
	seedRawcall(e, "req-1", at)
	e.Sandbox().SeedSegment("sess-1", e.ProjectHash(), at)
	e.Sandbox().SeedMetaSnapshot("sess-1", e.ProjectHash(), at)
	e.Sandbox().SeedGitSnapshot("sess-1", e.ProjectHash(), at)

	got := e.InProject("status")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Records waiting to upload: 1 rawcall(s), 1 segment(s), 1 session snapshot(s), 1 git snapshot(s)") {
		t.Errorf("stdout = %q, want every waiting kind counted", got.Stdout)
	}
}

func TestStatusCountsWaitingGitSnapshotsOnADeviceThatRecordsOnlyFromSessionHooks(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	e.Sandbox().SeedGitSnapshot("sess-1", e.ProjectHash(), time.Now().UTC())

	got := e.InProject("status")
	if !strings.Contains(got.Stdout, "Records waiting to upload: 1 git snapshot(s)") {
		t.Errorf("stdout = %q, want the waiting observation counted", got.Stdout)
	}
	if strings.Contains(got.Stdout, "waiting to upload: none") {
		t.Errorf("stdout = %q, want the wait reported by what is in the spool", got.Stdout)
	}
}

func TestStatusCountsWaitingRawcallsWhereNoSessionFileWasRead(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	seedRawcall(e, "req-1", time.Now().UTC())

	got := e.InProject("status")
	if !strings.Contains(got.Stdout, "Records waiting to upload: 1 rawcall(s)") {
		t.Errorf("stdout = %q, want the waiting rawcall counted", got.Stdout)
	}
	if strings.Contains(got.Stdout, "waiting to upload: none") {
		t.Errorf("stdout = %q, want the wait reported by what is in the spool", got.Stdout)
	}
}

func TestStatusWarnsAboutTheHookLeftInTheDirectoryClaudeCodeNoLongerReads(t *testing.T) {
	e := clitest.New(t)
	path := hookInDefaultClaudeDir(t, e)

	got := e.InProject("status")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "a trajector hook is left in ~/.claude/settings.json and never runs") {
		t.Errorf("stdout = %q, want the warning", got.Stdout)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hook discovery") {
		t.Errorf("settings file = %s, want status to have removed nothing", data)
	}
}

// TestStatusLeadsWithTheVerdictAndExitsOnWhatIsBroken pins the two
// things a caller reads without parsing the sections: the first line,
// and the exit code.
func TestStatusLeadsWithTheVerdictAndExitsOnWhatIsBroken(t *testing.T) {
	e := clitest.New(t)
	e.Paired()

	got := e.InProject("status")
	if got.Exit != 0 {
		t.Fatalf("exit = %d, want 0 on a device with nothing broken (stderr: %q)", got.Exit, got.Stderr)
	}
	if first, _, _ := strings.Cut(got.Stdout, "\n"); first != "Recording: off in this project" {
		t.Errorf("first line = %q, want the verdict", first)
	}

	e.Sandbox().Pause(proxytest.PauseSignedOut)
	got = e.InProject("status")
	if got.Exit != 1 {
		t.Errorf("exit = %d, want 1 while a pause stops recording (stdout: %q)", got.Exit, got.Stdout)
	}
	if first, _, _ := strings.Cut(got.Stdout, "\n"); first != "Recording: PAUSED on this device" {
		t.Errorf("first line = %q, want the verdict", first)
	}
	if !strings.Contains(got.Stdout, "error: ") || !strings.Contains(got.Stdout, "fix:  trajector login") {
		t.Errorf("stdout = %q, want the pause stated as an error with its one command", got.Stdout)
	}
}

// Whatever the terminal, nothing written to a pipe may need
// interpreting: a status output piped into a file is read by people and
// by scripts, and an escape sequence would be read by neither.
func TestStatusWritesNoEscapeSequenceToAPipe(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	e.Sandbox().Pause(proxytest.PauseSignedOut)

	for _, args := range [][]string{{"status"}, {"status", "--no-color"}} {
		got := e.InProject(args...)
		if strings.ContainsRune(got.Stdout, '\x1b') {
			t.Errorf("%v: stdout = %q, want no escape sequence", args, got.Stdout)
		}
		for _, r := range got.Stdout {
			if r > 0x7e {
				t.Errorf("%v: stdout = %q, want pure ASCII", args, got.Stdout)
				break
			}
		}
	}
}

// A routing table that exists and cannot be read leaves every grant
// unknown. Each command that reads it names the file and the steps out
// of it, status and doctor exit 1, and nothing is repaired or moved:
// the file is the only record of which projects are enabled.
func TestAnUnreadableRoutingTableIsNamedAndLeftAlone(t *testing.T) {
	tests := []struct {
		name  string
		spoil func(*proxytest.Sandbox)
		cause string
	}{
		{name: "a table that does not parse", spoil: (*proxytest.Sandbox).CorruptRoutingTable, cause: "unexpected end of JSON input"},
		{name: "a table no read succeeds on", spoil: (*proxytest.Sandbox).BlockRoutingTable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := clitest.New(t)
			e.Paired()
			p := e.StartProxy()
			defer p.Stop()
			if got := e.InProjectInput("yes\n", "enable"); got.Exit != 0 {
				t.Fatalf("enable = exit %d\nstdout: %s\nstderr: %s", got.Exit, got.Stdout, got.Stderr)
			}
			grant, ok := e.Sandbox().ActiveGrant(e.Project())
			if !ok {
				t.Fatal("enable left no grant")
			}
			settings := e.ProjectSettings().Contents()
			tt.spoil(e.Sandbox())
			table := e.Sandbox().RoutingTablePath()
			spoiled := tableState(t, table)
			named := []string{table, tt.cause, "Move that file aside", "`trajector enable` in each project you enabled"}

			status := e.InProject("status")
			if status.Exit != 1 {
				t.Errorf("status exit = %d, want 1 (stderr: %q)", status.Exit, status.Stderr)
			}
			if first, _, _ := strings.Cut(status.Stdout, "\n"); first != "Recording: STOPPED on this device (routing table unreadable)" {
				t.Errorf("first line = %q, want the verdict for an unreadable table", first)
			}
			wantAll(t, "status", status.Stdout, append(named, "error: recording is stopped: the routing table could not be read")...)
			if strings.Contains(status.Stdout, "Not enabled") {
				t.Errorf("status = %q, want no claim that the project is not enabled", status.Stdout)
			}

			doctor := e.InProject("doctor")
			if doctor.Exit != 1 {
				t.Errorf("doctor exit = %d, want 1 (stderr: %q)", doctor.Exit, doctor.Stderr)
			}
			wantAll(t, "doctor", doctor.Stdout, named...)
			if got := e.ProjectSettings().Contents(); got != settings {
				t.Errorf("doctor rewrote the injected settings:\nbefore: %s\nafter: %s", settings, got)
			}
			if got := tableState(t, table); got != spoiled {
				t.Errorf("the routing table went from %q to %q, want it left as it was", spoiled, got)
			}

			if bundle := e.InProject("doctor", "bundle"); bundle.Exit != 0 {
				t.Errorf("doctor bundle = exit %d (stderr: %q), want the archive written", bundle.Exit, bundle.Stderr)
			}

			enable := e.InProjectInput("yes\n", "enable")
			if enable.Exit != 1 {
				t.Errorf("enable exit = %d, want 1", enable.Exit)
			}
			wantAll(t, "enable", enable.Stderr, named...)

			if known, recording := e.Sandbox().Recording(grant.Token); known || recording {
				t.Errorf("the proxy's reading of the token: known = %v, recording = %v; want it unresolved and unrecorded", known, recording)
			}
		})
	}
}

// tableState is what is at the routing table's path: a directory, or
// the file's bytes.
func tableState(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		return "a directory"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func wantAll(t *testing.T, surface, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("%s = %q, want it to contain %q", surface, out, want)
		}
	}
}
