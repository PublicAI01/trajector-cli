package lifecycle

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
)

func TestWindowsHookRunsLiteralPathAndPassesInput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "space $literal 'quote' & [brackets] `tick")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(source, []byte(`package main
import ("fmt"; "io"; "os"; "strings")
func main() { body, err := io.ReadAll(os.Stdin); if err != nil { os.Exit(2) }; fmt.Printf("%s\n%s", strings.Join(os.Args[1:], ":"), body) }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "trajector.exe")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", exe, source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the argument echo fixture: %v\n%s", err, out)
	}
	command := hookCommand(exe, claudesettings.HookProgress)
	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skip("this PowerShell edition is not installed")
			}
			cmd := exec.CommandContext(t.Context(), shell, "-NoProfile", "-NonInteractive", "-Command", command)
			cmd.Stdin = strings.NewReader(`{"session_id":"synthetic"}`)
			out, err := cmd.CombinedOutput()
			if err != nil || strings.ReplaceAll(string(out), "\r\n", "\n") != "hook:progress\n"+`{"session_id":"synthetic"}` {
				t.Fatalf("hook did not preserve its path, arguments and stdin: %v\n%s", err, out)
			}
		})
	}
}
