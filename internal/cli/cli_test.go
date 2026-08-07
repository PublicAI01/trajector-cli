package cli_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/routing"
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
	e.Sandbox().Pause(routing.PauseSignedOut)

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
