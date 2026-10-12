package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/proxylife"
)

// Claude Code shows a hook that exits non-zero as one red line, the
// first of its stderr, and lets the session go on; exit 2 would block
// the prompt instead. So the sentence is that first line, whole, and
// the exit is 1. Only a session whose requests go through the proxy is
// told next that they fail.
func TestASessionHookSaysFirstThatTheBuildCannotStartItsProxy(t *testing.T) {
	const first = "trajector 0.4.0 cannot start its proxy, so trajector does not work until it is reinstalled: see Installation in README."
	const routed = "Requests from this project fail until then."
	for _, tc := range []struct {
		name   string
		routed bool
		want   []string
	}{
		{"requests go through the proxy", true, []string{first, routed}},
		{"the project records without the proxy", false, []string{first}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr strings.Builder
			a := &app{stderr: &stderr}

			exit := a.cannotStart(&proxylife.CannotStartError{
				Version: "0.4.0",
				Cause:   errors.New("proxy did not become healthy at 127.0.0.1:41100 within 10s"),
			}, tc.routed)

			if exit != 1 {
				t.Errorf("exit = %d, want 1", exit)
			}
			lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
			if len(lines) != len(tc.want)+1 {
				t.Fatalf("stderr = %q, want %q then the last start's cause", stderr.String(), tc.want)
			}
			for i, want := range tc.want {
				if lines[i] != want {
					t.Errorf("stderr line %d = %q, want %q", i+1, lines[i], want)
				}
			}
			if last := lines[len(lines)-1]; !strings.Contains(last, "did not become healthy") {
				t.Errorf("last stderr line = %q, want the last start's cause", last)
			}
		})
	}
}
