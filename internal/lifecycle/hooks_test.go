package lifecycle_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestDiscoveryPromptsOncePerProject(t *testing.T) {
	e := newEnv(t)

	e.machine().Discovery(e.project, e.io())
	if !strings.Contains(e.stdout.String(), "trajector enable") {
		t.Fatalf("first hint = %q", e.stdout)
	}
	e.stdout.Reset()

	e.machine().Discovery(e.project, e.io())
	if e.stdout.String() != "" {
		t.Errorf("second hint = %q, want silence", e.stdout)
	}
}

func TestDiscoveryIsSilentForAnEnabledProject(t *testing.T) {
	e := newEnv(t)
	e.sandbox.GrantProject(proxytest.Grant{
		Token:         "tok-proj",
		ProjectIDHash: proxytest.ProjectIDHash(e.canonicalRoot()),
		RootPath:      e.canonicalRoot(),
		Upstream:      "https://api.anthropic.com",
	})

	e.machine().Discovery(e.project, e.io())
	if e.stdout.String() != "" || e.stderr.String() != "" {
		t.Errorf("enabled project got %q / %q, want total silence", e.stdout, e.stderr)
	}
}

func TestDiscoveryMarkerNeverStoresThePath(t *testing.T) {
	e := newEnv(t)
	e.machine().Discovery(e.project, e.io())

	stored := e.consentFileContents()
	root := e.canonicalRoot()
	if strings.Contains(stored, root) {
		t.Errorf("consent file leaks the project path: %s", stored)
	}
	if !strings.Contains(stored, proxytest.ProjectIDHash(root)) {
		t.Errorf("consent file lacks the project hash: %s", stored)
	}
}

func TestEnsureProxyPausesRecordingOnAStaleAgreement(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.sandbox.AcceptAgreement("2020-01-obsolete", "2020-01-01T00:00:00Z")

	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatalf("ensure-proxy: %v", err)
	}
	if reason := e.sandbox.PausedReason(); reason != proxytest.PauseConsentReconfirm {
		t.Errorf("pause = %q, want %q", reason, proxytest.PauseConsentReconfirm)
	}
	if !strings.Contains(e.stderr.String(), proxytest.PauseConsentReconfirm.Explain()) {
		t.Errorf("stderr = %q, want the pause reason's own sentence", e.stderr)
	}
}

func TestEnsureProxyFollowsUpstreamDrift(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, proxytest.WithProxy, e.io()); err != nil {
		t.Fatal(err)
	}

	e.environ["ANTHROPIC_BASE_URL"] = "https://relay.example.com"
	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	grant := e.status()
	if !grant.Enabled || grant.Upstream != "https://relay.example.com" {
		t.Errorf("grant = %+v, want the relay picked up", grant)
	}

	delete(e.environ, "ANTHROPIC_BASE_URL")
	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	grant = e.status()
	if grant.Upstream != "https://api.anthropic.com" {
		t.Errorf("upstream after the relay is removed = %q, want the official one", grant.Upstream)
	}
}

func TestUnsupportedChannelIsReportedNotRewritten(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.environ["ANTHROPIC_BASE_URL"] = "https://relay.example.com"
	if err := e.machine().Enable(e.project, proxytest.WithProxy, e.io()); err != nil {
		t.Fatal(err)
	}
	// The user moves the project to Bedrock after enabling it. From here
	// on there is one answer to "where should traffic go": the channel is
	// unsupported, so nothing may rewrite what enable recorded.
	delete(e.environ, "ANTHROPIC_BASE_URL")
	e.environ["CLAUDE_CODE_USE_BEDROCK"] = "1"

	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if got := e.status().Upstream; got != "https://relay.example.com" {
		t.Errorf("the session hook rewrote the upstream to %q", got)
	}

	e.stdout.Reset()
	problems, err := e.machine().Doctor(e.project, e.io())
	if err != nil {
		t.Fatal(err)
	}
	if problems == 0 {
		t.Error("doctor found no problem on a Bedrock channel")
	}
	if !strings.Contains(e.stdout.String(), "CLAUDE_CODE_USE_BEDROCK") {
		t.Errorf("doctor output does not name the Bedrock setting: %s", e.stdout)
	}
	if got := e.status().Upstream; got != "https://relay.example.com" {
		t.Errorf("doctor rewrote the upstream to %q", got)
	}
}

func TestEnsureProxyRefusesAForeignPortHolder(t *testing.T) {
	e := newEnv(t)
	e.occupyPort()

	err := e.machine().EnsureProxy(e.project, e.io())
	if err == nil {
		t.Fatal("ensure-proxy succeeded against a foreign port holder")
	}
	if !strings.Contains(err.Error(), "not the trajector proxy") {
		t.Errorf("err = %v", err)
	}
}

// TestEnsureProxyPausesRecordingWhenConsentCannotBeRead pins the fix for
// the fail-open agreement gate: a consent store that will not parse used
// to take the same silent return as "your agreement is current", so
// every enabled project kept recording under terms that could no longer
// be shown to have been accepted.
func TestEnsureProxyPausesRecordingWhenConsentCannotBeRead(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.acceptCurrentAgreement()
	e.sandbox.CorruptConsent()

	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatalf("ensure-proxy: %v", err)
	}
	if reason := e.sandbox.PausedReason(); reason != proxytest.PauseConsentUnreadable {
		t.Errorf("pause = %q, want %q; an unreadable consent store must not read as a current agreement", reason, proxytest.PauseConsentUnreadable)
	}
	if !strings.Contains(e.stderr.String(), "could not be read") {
		t.Errorf("stderr = %q, want the unreadable consent store explained", e.stderr)
	}
	if !strings.Contains(e.stderr.String(), e.sandbox.ConsentPath()) {
		t.Errorf("stderr = %q, want the consent record's own path named", e.stderr)
	}
	if !strings.Contains(e.stderr.String(), "`trajector enable`") {
		t.Errorf("stderr = %q, want the command that writes a new record", e.stderr)
	}
}

func TestEnableResumesBothConsentPauses(t *testing.T) {
	for _, reason := range []proxytest.PauseReason{proxytest.PauseConsentReconfirm, proxytest.PauseConsentUnreadable} {
		t.Run(string(reason), func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			e.sandbox.CorruptConsent()
			e.sandbox.Pause(reason)
			e.stdin = "yes\nyes\nyes\n"

			if err := e.machine().Enable(e.project, proxytest.WithProxy, e.io()); err != nil {
				t.Fatalf("enable: %v", err)
			}
			if got := e.sandbox.PausedReason(); got != "" {
				t.Errorf("pause = %q, want recording resumed", got)
			}
		})
	}
}
