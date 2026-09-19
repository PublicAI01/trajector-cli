package cli_test

import (
	"os"
	"strings"
	"testing"
	"time"

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
