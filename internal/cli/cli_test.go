package cli_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestRootCommand(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantExit     int
		wantStdout   string
		wantInStderr string
	}{
		{
			name:       "version",
			args:       []string{"version"},
			wantExit:   0,
			wantStdout: "trajector dev\n",
		},
		{
			name:       "version flag spelling",
			args:       []string{"--version"},
			wantExit:   0,
			wantStdout: "trajector dev\n",
		},
		{
			name:         "no arguments prints usage",
			args:         nil,
			wantExit:     2,
			wantInStderr: "usage: trajector",
		},
		{
			name:         "unknown command",
			args:         []string{"frobnicate"},
			wantExit:     2,
			wantInStderr: `unknown command "frobnicate"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := clitest.New(t)
			got := e.Run(tt.args...)
			if got.Exit != tt.wantExit {
				t.Errorf("exit = %d, want %d (stderr: %q)", got.Exit, tt.wantExit, got.Stderr)
			}
			if got.Stdout != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", got.Stdout, tt.wantStdout)
			}
			if !strings.Contains(got.Stderr, tt.wantInStderr) {
				t.Errorf("stderr = %q, want it to contain %q", got.Stderr, tt.wantInStderr)
			}
		})
	}
}

func TestCommandsRejectStrayArguments(t *testing.T) {
	tests := []struct {
		args      []string
		wantUsage string
	}{
		{[]string{"login", "extra"}, "usage: trajector login"},
		{[]string{"logout", "extra"}, "usage: trajector logout"},
		{[]string{"enable", "extra"}, "usage: trajector enable"},
		{[]string{"disable", "--wrong"}, "usage: trajector disable"},
		{[]string{"uninstall", "extra"}, "usage: trajector uninstall"},
		{[]string{"hook"}, "usage: trajector hook"},
		{[]string{"hook", "one", "two"}, "usage: trajector hook"},
		{[]string{"proxy"}, "usage: trajector proxy"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			e := clitest.New(t)
			got := e.Run(tt.args...)
			if got.Exit != 2 {
				t.Errorf("exit = %d, want 2 for a usage error", got.Exit)
			}
			if !strings.Contains(got.Stderr, tt.wantUsage) {
				t.Errorf("stderr = %q, want it to contain %q", got.Stderr, tt.wantUsage)
			}
		})
	}
}

func TestUnknownSubcommandsAreUsageErrors(t *testing.T) {
	e := clitest.New(t)
	if got := e.Run("hook", "frobnicate"); got.Exit != 2 || !strings.Contains(got.Stderr, "unknown hook") {
		t.Errorf("unknown hook = %+v", got)
	}
	if got := e.Run("proxy", "frobnicate"); got.Exit != 2 || !strings.Contains(got.Stderr, "unknown proxy command") {
		t.Errorf("unknown proxy command = %+v", got)
	}
}

func TestEnableExplainsAPausedDevice(t *testing.T) {
	e := clitest.New(t)
	e.StartProxy()
	// A paired device whose recording is paused device-wide.
	e.Paired()
	e.Sandbox().Pause(proxytest.PauseSignedOut)

	got := e.InProjectInput("yes\n", "enable")
	if got.Exit != 1 {
		t.Fatalf("exit = %d, want 1 (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stderr, "trajector login") {
		t.Errorf("stderr = %q, want the signed-out pause explained with the command to run", got.Stderr)
	}
}

func TestEnableReportsServiceFailureWithExitCodeOne(t *testing.T) {
	e := clitest.New(t)
	got := e.InProjectInput("yes\n", "enable")
	if got.Exit != 1 {
		t.Errorf("enable against a failing service = %d, want 1", got.Exit)
	}
	if !strings.Contains(got.Stderr, "trajector: ") {
		t.Errorf("stderr = %q, want the failure reported once, prefixed", got.Stderr)
	}
}

func TestCommandsAnswerHelpWithUsage(t *testing.T) {
	tests := []struct {
		args      []string
		wantUsage string
	}{
		{[]string{"--help"}, "usage: trajector <command>"},
		{[]string{"-h"}, "usage: trajector <command>"},
		{[]string{"forget", "--help"}, "usage: trajector forget <session-id>"},
		{[]string{"forget", "-h"}, "usage: trajector forget <session-id>"},
		{[]string{"doctor", "--help"}, "usage: trajector doctor"},
		{[]string{"doctor", "discard", "--help"}, "usage: trajector doctor discard"},
		{[]string{"doctor", "requeue", "--help"}, "usage: trajector doctor requeue"},
		{[]string{"upload", "--help"}, "usage: trajector upload"},
		{[]string{"enable", "--help"}, "usage: trajector enable"},
		{[]string{"disable", "--help"}, "usage: trajector disable"},
		{[]string{"uninstall", "--help"}, "usage: trajector uninstall"},
		{[]string{"status", "--help"}, "usage: trajector status"},
		{[]string{"version", "--help"}, "usage: trajector version"},
		{[]string{"hook", "read", "--help"}, "usage: trajector hook"},
		{[]string{"proxy", "run", "--help"}, "usage: trajector proxy run"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			e := clitest.New(t)
			got := e.Run(tt.args...)
			if got.Exit != 0 {
				t.Errorf("exit = %d, want 0 (stderr: %q)", got.Exit, got.Stderr)
			}
			if !strings.Contains(got.Stdout, tt.wantUsage) {
				t.Errorf("stdout = %q, want it to contain %q", got.Stdout, tt.wantUsage)
			}
			if got.Stderr != "" {
				t.Errorf("stderr = %q, want help on stdout alone", got.Stderr)
			}
		})
	}
}

func TestCommandsRefuseAnUnknownFlag(t *testing.T) {
	tests := []struct {
		args      []string
		wantUsage string
	}{
		{[]string{"forget", "--bogus"}, "usage: trajector forget <session-id>"},
		{[]string{"doctor", "--bogus"}, "usage: trajector doctor"},
		{[]string{"doctor", "discard", "--bogus"}, "usage: trajector doctor discard"},
		{[]string{"upload", "--bogus"}, "usage: trajector upload"},
		{[]string{"enable", "--bogus"}, "usage: trajector enable"},
		{[]string{"status", "--bogus"}, "usage: trajector status"},
		{[]string{"version", "--bogus"}, "usage: trajector version"},
		{[]string{"hook", "read", "--bogus"}, "usage: trajector hook"},
		{[]string{"proxy", "run", "--bogus"}, "usage: trajector proxy run"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			e := clitest.New(t)
			got := e.Run(tt.args...)
			if got.Exit != 2 {
				t.Errorf("exit = %d, want 2 (stdout: %q)", got.Exit, got.Stdout)
			}
			if !strings.Contains(got.Stderr, tt.wantUsage) {
				t.Errorf("stderr = %q, want it to contain %q", got.Stderr, tt.wantUsage)
			}
			if !strings.Contains(got.Stderr, "unknown flag") {
				t.Errorf("stderr = %q, want the refused flag named", got.Stderr)
			}
		})
	}
}

func TestKnownFlagsStillReachTheirCommands(t *testing.T) {
	tests := [][]string{
		{"doctor", "requeue", "--all"},
		{"doctor", "discard", "--all", "--yes"},
		{"upload", "--force"},
		{"hook", "ensure-proxy", "--no-proxy"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e := clitest.New(t)
			got := e.Run(args...)
			if strings.Contains(got.Stderr, "unknown flag") || strings.Contains(got.Stderr, "usage:") {
				t.Errorf("stderr = %q, want the command to run rather than answer with its usage", got.Stderr)
			}
		})
	}
}

func TestHookReadAnsweringHelpTouchesNothing(t *testing.T) {
	e := clitest.New(t)

	got := e.Run("hook", "read", "--help")

	if got.Exit != 0 {
		t.Errorf("exit = %d, want 0 (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "usage: trajector hook") {
		t.Errorf("stdout = %q, want the hook usage", got.Stdout)
	}
	if registered := e.Sandbox().RegisteredPaths(e.ProjectHash()); len(registered) != 0 {
		t.Errorf("registry = %q, want nothing registered", registered)
	}
	if starts := e.ProxyStarts(); len(starts) != 0 {
		t.Errorf("proxy starts = %q, want none", starts)
	}
}
