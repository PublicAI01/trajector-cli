package lifecycle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
)

// neverComesUp reports a started process and starts none: from outside,
// a build whose proxy dies on this machine's state before it serves.
func neverComesUp(context.Context, string, []string, string) (int, error) { return 4242, nil }

// A session that takes over from an older proxy and cannot bring its
// own up is told so, and status and doctor say the same sentence
// afterwards: the build itself still runs, only its proxy does not.
func TestASessionStatusAndDoctorNameABuildThatCannotStartItsProxy(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "2.0.0"
	e.deps.Spawn = neverComesUp
	e.startProxy(proxytest.WithVersion("1.0.0"))

	_, err := e.machine().SessionStarting(t.Context(), e.project, lifecycle.HookInput{}, e.io())
	if cannot, ok := errors.AsType[*proxylife.CannotStartError](err); !ok || cannot.Version != "2.0.0" {
		t.Fatalf("SessionStarting = %v, want a CannotStartError naming 2.0.0", err)
	}
	if err := e.proxyEnv.WaitStopped(5 * time.Second); err != nil {
		t.Errorf("Serve = %v after the takeover, want the older proxy drained", err)
	}

	want := report.CannotStart("2.0.0")
	if out := e.statusOutput(); !strings.Contains(out, want) {
		t.Errorf("status = %q, want %q", out, want)
	}
	e.stdout.Reset()
	problems, out := e.doctor()
	if problems == 0 || !strings.Contains(out, want) {
		t.Errorf("doctor (%d problems) = %q, want %q counted as a problem", problems, out, want)
	}
}
