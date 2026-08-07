package cli_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
)

func TestUninstallKeepsDataWhenDeclined(t *testing.T) {
	e := clitest.New(t)
	e.Sandbox().SeedRawcall("req-1", "hash-project", time.Now().UTC())

	got := e.RunInput("n\n", "uninstall")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Local data kept.") {
		t.Errorf("stdout = %q, want the kept-data notice", got.Stdout)
	}
	if !strings.Contains(got.Stdout, "delete the binary itself") {
		t.Errorf("stdout = %q, want the final binary hint", got.Stdout)
	}
	if n := len(e.Sandbox().Rawcalls()); n != 1 {
		t.Errorf("spool holds %d rawcall(s) after a declined uninstall, want 1", n)
	}
}

func TestUninstallDeletesDataWhenConfirmed(t *testing.T) {
	e := clitest.New(t)
	e.Sandbox().SeedRawcall("req-1", "hash-project", time.Now().UTC())

	got := e.RunInput("y\n", "uninstall")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "Local data deleted.") {
		t.Errorf("stdout = %q, want the deleted-data notice", got.Stdout)
	}
	if _, err := os.Stat(e.Layout().SpoolDir()); !os.IsNotExist(err) {
		t.Errorf("spool directory survived a confirmed uninstall (stat: %v)", err)
	}
}

func TestUninstallDeleteDataFlagSkipsThePrompt(t *testing.T) {
	e := clitest.New(t)
	e.Sandbox().SeedRawcall("req-1", "hash-project", time.Now().UTC())

	got := e.Run("uninstall", "--delete-data")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if strings.Contains(got.Stdout, "[y/N]") {
		t.Errorf("stdout = %q, want no prompt with the flag", got.Stdout)
	}
	if !strings.Contains(got.Stdout, "Local data deleted.") {
		t.Errorf("stdout = %q, want the deleted-data notice", got.Stdout)
	}
	if _, err := os.Stat(e.Layout().SpoolDir()); !os.IsNotExist(err) {
		t.Errorf("spool directory survived --delete-data (stat: %v)", err)
	}
}
