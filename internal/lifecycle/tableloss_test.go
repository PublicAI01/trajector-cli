package lifecycle_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

func TestEnableRefusesToGuessARelayOnceTheRoutingTableIsMovedAside(t *testing.T) {
	e := enabledOverAUsersOwnRelay(t)
	injected := e.projectSettings().Contents()
	e.sandbox.MoveRoutingTableAside()

	e.stdout.Reset()
	err := e.machine().Enable(e.project, choices(proxytest.WithProxy), e.io())
	if !errors.Is(err, lifecycle.ErrInjectionWithoutGrant) {
		t.Fatalf("enable with the table moved aside returned %v; want a refusal", err)
	}
	for _, want := range []string{"ANTHROPIC_BASE_URL", "`upstream`", "delete that ANTHROPIC_BASE_URL line"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %q, want it to say %q", err, want)
		}
	}
	if g, ok := e.sandbox.ActiveGrant(e.project); ok {
		t.Errorf("a refused enable granted the project at %q; the relay was replaced by a guess", g.Upstream)
	}
	if got := e.projectSettings().Contents(); got != injected {
		t.Errorf("a refused enable rewrote the settings:\nbefore: %s\nafter: %s", injected, got)
	}
}

func TestDoctorLeavesAnInjectionNoGrantRecordsInPlace(t *testing.T) {
	e := enabledOverAUsersOwnRelay(t)
	injected := e.projectSettings().Contents()
	e.sandbox.MoveRoutingTableAside()

	e.stdout.Reset()
	problems, out := e.doctor()
	if problems == 0 {
		t.Errorf("doctor found nothing wrong, output:\n%s", out)
	}
	for _, want := range []string{"no grant for this project", "`upstream`", "delete that ANTHROPIC_BASE_URL line"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor = %q, want it to say %q", out, want)
		}
	}
	if strings.Contains(out, "removed a stale injection") {
		t.Errorf("doctor = %q, want the injection left alone", out)
	}
	if got := e.projectSettings().Contents(); got != injected {
		t.Errorf("doctor rewrote the settings:\nbefore: %s\nafter: %s", injected, got)
	}
}

func TestEnableGrantsTheRelayWrittenBackAfterTheRoutingTableIsMovedAside(t *testing.T) {
	e := enabledOverAUsersOwnRelay(t)
	e.sandbox.MoveRoutingTableAside()
	e.projectSettings().Put(`{"env":{"ANTHROPIC_BASE_URL":"` + relayInSettingsLocal + `"}}`)

	if err := e.machine().Enable(e.project, choices(proxytest.WithProxy), e.io()); err != nil {
		t.Fatalf("enable after the relay was written back: %v\nstdout: %s", err, e.stdout)
	}
	if got := e.status().Upstream; got != relayInSettingsLocal {
		t.Errorf("upstream = %q, want the relay written back, %q", got, relayInSettingsLocal)
	}
}
