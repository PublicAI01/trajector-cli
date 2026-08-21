package lifecycle_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

// injectedBaseURL is the base URL enable wrote into the project's
// settings — the value Claude Code exports to everything it spawns,
// the session hook included.
func injectedBaseURL(t *testing.T, e *env) string {
	t.Helper()
	data, err := os.ReadFile(e.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	value := settings.Env["ANTHROPIC_BASE_URL"]
	if value == "" {
		t.Fatalf("test setup: no base URL injected into %s", e.settingsPath())
	}
	return value
}

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

func TestUpstreamDriftLeavesAVisibleTrace(t *testing.T) {
	e := enabledOnOfficial(t)
	official := e.status().Upstream
	e.environ["ANTHROPIC_BASE_URL"] = "https://relay.example.com"

	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stderr.String(), "upstream moved to https://relay.example.com") {
		t.Errorf("hook stderr = %q, want the move announced", e.stderr)
	}
	st := e.status()
	if st.UpstreamMoved.From != official || st.UpstreamMoved.At == "" {
		t.Errorf("status = moved from %q at %q, want the previous upstream and a time", st.UpstreamMoved.From, st.UpstreamMoved.At)
	}

	e.stdout.Reset()
	if err := e.machine().Status(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if out := e.stdout.String(); !strings.Contains(out, "moved from "+official) {
		t.Errorf("status output = %q, want the move shown", out)
	}
}

func TestReEnableResetsTheUpstreamMoveTrace(t *testing.T) {
	e := enabledOnOfficial(t)
	e.environ["ANTHROPIC_BASE_URL"] = "https://relay.example.com"
	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	st := e.status()
	if st.UpstreamMoved.Happened() {
		t.Errorf("status after re-enable = moved from %q at %q, want the trace reset", st.UpstreamMoved.From, st.UpstreamMoved.At)
	}
}

func TestHookAnnouncesARefusedUpstreamMove(t *testing.T) {
	e := enabledOnOfficial(t)
	e.environ["ANTHROPIC_BASE_URL"] = "http://203.0.113.9"

	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stderr.String(), "must use https") {
		t.Errorf("hook stderr = %q, want the refusal explained", e.stderr)
	}
}

func TestASessionsOwnInjectionIsNotReadAsTheRelayBeingGone(t *testing.T) {
	// The user's relay is exported from their shell, which is where
	// enable finds it. Then a session starts: Claude Code applies the
	// settings env block, so the hook it spawns sees our own proxy URL
	// standing where the relay was. Reading that as "the relay is gone"
	// would point the grant at the official endpoint on the very first
	// session — and every request after it would carry the relay's
	// credentials to a service that rejects them.
	const relay = "https://relay.example.com"
	e := newEnv(t)
	e.startProxy()
	e.environ["ANTHROPIC_BASE_URL"] = relay
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if got := e.status().Upstream; got != relay {
		t.Fatalf("upstream after enable = %q, want the relay recorded", got)
	}

	e.environ["ANTHROPIC_BASE_URL"] = injectedBaseURL(t, e)
	e.stderr.Reset()
	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if got := e.status().Upstream; got != relay {
		t.Errorf("upstream after a session started = %q, want the relay kept", got)
	}
	if moved := e.status().UpstreamMoved; moved.Happened() {
		t.Errorf("status reports a move from %q, want no move to report", moved.From)
	}
	if strings.Contains(e.stderr.String(), "upstream moved") {
		t.Errorf("hook stderr = %q, want no move announced", e.stderr)
	}

	// doctor answers from the same resolution, so a user who runs it
	// inside that session must not be told to worry either.
	e.stdout.Reset()
	problems, err := e.machine().Doctor(e.project, e.io())
	if err != nil {
		t.Fatal(err)
	}
	if problems != 0 || strings.Contains(e.stdout.String(), "upstream") {
		t.Errorf("doctor = %d problem(s), output %q; want it quiet about the upstream", problems, e.stdout)
	}
	if got := e.status().Upstream; got != relay {
		t.Errorf("upstream after doctor = %q, want the relay kept", got)
	}
}

// TestEnableRefusesToGuessAMaskedUpstream is the granting half of what
// TestASessionsOwnInjectionIsNotReadAsTheRelayBeingGone pins for the
// hook. The relay lives in the user's shell; once a session starts, our
// own injection stands where it was, so the chain reads as masked. With
// no standing grant to fall back on — a fresh project, or this one right
// after disable — enable has nothing that says where the traffic went.
// Until 2026-08-16 it granted the official endpoint anyway, and every
// later request carried the relay's credentials to Anthropic. Refusing
// is the only answer that does not guess.
func TestEnableRefusesToGuessAMaskedUpstream(t *testing.T) {
	const relay = "https://relay.example.com"
	e := newEnv(t)
	e.startProxy()
	e.environ["ANTHROPIC_BASE_URL"] = relay
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	// A session is running now, so the CLI's own environment carries our
	// injection rather than the relay. The user disables from inside it.
	e.environ["ANTHROPIC_BASE_URL"] = injectedBaseURL(t, e)
	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatal(err)
	}

	e.stdout.Reset()
	err := e.machine().Enable(e.project, e.io())
	if !errors.Is(err, lifecycle.ErrUpstreamMasked) {
		t.Fatalf("re-enable with the upstream masked returned %v and granted %q; the relay was replaced by a guess",
			err, e.status().Upstream)
	}
	if st := e.status(); st.Enabled || st.Injected() {
		t.Errorf("a refused enable left the project enabled=%v injected=%v, want nothing written", st.Enabled, st.Injected())
	}
}

// TestOurOwnInjectionIsNotReadAsTheRelayBeingGone is the settings-file
// spelling of what TestASessionsOwnInjectionIsNotReadAsTheRelayBeingGone
// pins for the shell. The user keeps their relay in the project-local
// settings file — the very file, and the very key, injection writes
// into — so after enable the chain names nothing of the user's own from
// anywhere, session or not. Reading that silence as "the relay is gone"
// moved the grant to the official endpoint, and every later request
// carried the relay's credentials to Anthropic while its records were
// relabelled official origin. masked covers only the shell spelling;
// until 2026-08-21 nothing covered this one outside enable.
func TestOurOwnInjectionIsNotReadAsTheRelayBeingGone(t *testing.T) {
	e := enabledOverAUsersOwnRelay(t)

	// A plain terminal: no session applied our env block, so the shell
	// carries nothing at all and the chain reads as empty.
	e.stderr.Reset()
	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if got := e.status().Upstream; got != relayInSettingsLocal {
		t.Errorf("upstream after the session hook = %q, want the relay kept", got)
	}

	e.stdout.Reset()
	if _, err := e.machine().Doctor(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if got := e.status().Upstream; got != relayInSettingsLocal {
		t.Errorf("upstream after doctor = %q, want the relay kept", got)
	}
	if st := e.status(); st.UpstreamMoved.Happened() {
		t.Errorf("a move from %q was recorded, want none: nothing of the user's own moved", st.UpstreamMoved.From)
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
