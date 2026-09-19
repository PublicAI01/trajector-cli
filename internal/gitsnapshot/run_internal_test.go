package gitsnapshot

import (
	"cmp"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// seedRepository builds a repository of the test's own with one commit
// in it. Nothing here touches a repository the developer owns: the
// directory and the configuration both live in the test's temp tree.
func seedRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on this machine, so there is nothing to observe with")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.invalid"},
		{"commit", "--allow-empty", "-m", "seed"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

func TestRunRefusesAnArgvThatIsNotOneOfTheReadOnlySubcommands(t *testing.T) {
	dir := seedRepository(t)
	o := Observer{Dir: dir}
	before, err := o.Position(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, argv := range [][]string{
		{"commit", "--allow-empty", "-m", "a caller that should not have"},
		{"checkout", "-b", "another"},
		{"config", "user.email", "someone@example.invalid"},
		{"gc", "--prune=all"},
		{"fetch", "origin"},
		nil,
	} {
		t.Run(cmp.Or(strings.Join(argv, " "), "no subcommand"), func(t *testing.T) {
			if _, err := o.run(t.Context(), argv...); !errors.Is(err, errNotReadOnly) {
				t.Errorf("err = %v, want the argv refused before git runs", err)
			}
		})
	}

	after, err := o.Position(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("position = %+v, want the repository still at %+v", after, before)
	}
}
