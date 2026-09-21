package proxylife

import (
	"os/exec"
	"runtime"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
)

// proxyShaped starts a process whose program file and command line are
// the ones a proxy this build starts itself carries.
func proxyShaped(t *testing.T, exe string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(exe, Command, Serve)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestTheReadingByPIDProvesAProxyOnlyWhileItsProcessRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this device reads nothing about a process on Windows")
	}
	exe := procbin.Self(t, "sleep")
	running := proxyShaped(t, exe)
	t.Cleanup(func() {
		running.Process.Kill()
		running.Wait()
	})
	gone := proxyShaped(t, exe)
	gone.Process.Kill()
	gone.Wait()

	for _, tc := range []struct {
		name string
		pid  int
		want bool
	}{
		{name: "the process that runs the program file and the serve command", pid: running.Process.Pid, want: true},
		{name: "a pid whose process is gone", pid: gone.Process.Pid, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := describe(tc.pid).IsProxyOf(exe); got != tc.want {
				t.Errorf("IsProxyOf = %v, want %v", got, tc.want)
			}
		})
	}
}
