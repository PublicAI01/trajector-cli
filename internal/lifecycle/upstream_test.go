package lifecycle_test

import (
	"strings"
	"testing"
)

// enabledOnOfficial is an enabled project whose recorded upstream is
// the official endpoint, ready for a drift to be played against it.
func enabledOnOfficial(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestHookAppliesUpstreamDriftToSafeDestinations(t *testing.T) {
	tests := []struct {
		name     string
		upstream string
	}{
		{"https anywhere", "https://relay.example.com"},
		{"plaintext loopback", "http://127.0.0.1:9999"},
		{"plaintext localhost", "http://localhost:9999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := enabledOnOfficial(t)
			e.environ["ANTHROPIC_BASE_URL"] = tt.upstream

			if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
				t.Fatal(err)
			}
			if got := e.status().Upstream; got != tt.upstream {
				t.Errorf("upstream = %q, want the drift applied to %q", got, tt.upstream)
			}
		})
	}
}

func TestHookRefusesUpstreamDriftToPlaintextNonLoopback(t *testing.T) {
	e := enabledOnOfficial(t)
	before := e.status().Upstream
	e.environ["ANTHROPIC_BASE_URL"] = "http://203.0.113.9"

	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if got := e.status().Upstream; got != before {
		t.Errorf("upstream = %q, want %q kept: credentialed traffic must not drift to a plaintext non-loopback host", got, before)
	}
}

func TestDoctorReportsARefusedUpstreamDrift(t *testing.T) {
	e := enabledOnOfficial(t)
	before := e.status().Upstream
	e.environ["ANTHROPIC_BASE_URL"] = "http://203.0.113.9"

	problems, err := e.machine().Doctor(e.project, e.io())
	if err != nil {
		t.Fatal(err)
	}
	if problems == 0 {
		t.Error("doctor reported no problems for a refused upstream move")
	}
	if !strings.Contains(e.stdout.String(), "https") {
		t.Errorf("doctor output = %q, want the https requirement named", e.stdout)
	}
	if got := e.status().Upstream; got != before {
		t.Errorf("upstream = %q, want %q kept by doctor as well", got, before)
	}
}
