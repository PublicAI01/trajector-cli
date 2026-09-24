package cli_test

import (
	"os"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
)

func TestHookRead_StoresOnlyWhatASessionWroteInsideTheProject(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	path := earlierSessionFile(t, e, "0f1e2d3c")
	if got := e.InProjectInput("yes\n", "enable", "--no-proxy"); got.Exit != 0 {
		t.Fatalf("enable = exit %d\nstdout: %s\nstderr: %s", got.Exit, got.Stdout, got.Stderr)
	}
	root := e.ProjectRoot()
	appendLines(t, path,
		`{"type":"user","message":{"role":"user","content":"inside-one"}}`,
		`{"type":"relocated","relocatedCwd":"/elsewhere/away-one"}`,
		`{"type":"user","message":{"role":"user","content":"outside-one"}}`,
		`{"type":"relocated","relocatedCwd":"`+root+`"}`,
		`{"type":"user","message":{"role":"user","content":"inside-two"}}`,
		`{"type":"relocated","relocatedCwd":"/elsewhere/away-two"}`,
		`{"type":"user","message":{"role":"user","content":"outside-two"}}`,
	)

	if got := e.Run("hook", "read", e.Project()); got.Exit != 0 {
		t.Fatalf("hook read = exit %d, stderr: %s", got.Exit, got.Stderr)
	}

	var stored strings.Builder
	for _, r := range e.Sandbox().Records() {
		stored.Write(r.Raw)
	}
	for _, want := range []string{"inside-one", "inside-two"} {
		if !strings.Contains(stored.String(), want) {
			t.Errorf("stored records lack the line %q written inside the project", want)
		}
	}
	for _, unwanted := range []string{"outside-one", "outside-two", "away-one", "away-two"} {
		if strings.Contains(stored.String(), unwanted) {
			t.Errorf("stored records hold %q, which the session wrote outside the project", unwanted)
		}
	}
	status := e.InProject("status")
	if !strings.Contains(status.Stdout, "Outside this project: 1 session(s)") {
		t.Errorf("status does not count the session outside the project:\n%s", status.Stdout)
	}
	if strings.Contains(status.Stdout, "/elsewhere") {
		t.Errorf("status names where the session went:\n%s", status.Stdout)
	}
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}
