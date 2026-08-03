package lifecycle_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/consent"
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
		ProjectIDHash: consent.ProjectIDHash(e.canonicalRoot()),
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
	if !strings.Contains(stored, consent.ProjectIDHash(root)) {
		t.Errorf("consent file lacks the project hash: %s", stored)
	}
}

func TestEnsureProxyPausesRecordingOnAStaleAgreement(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.consentStore().AcceptAgreement("2020-01-obsolete", "2020-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	if err := e.machine().EnsureProxy(e.project, e.io()); err != nil {
		t.Fatalf("ensure-proxy: %v", err)
	}
	if reason := e.sandbox.PausedReason(); reason != "consent_reconfirm" {
		t.Errorf("pause = %q, want the reconfirmation pause", reason)
	}
	if !strings.Contains(e.stderr.String(), "agreement changed") {
		t.Errorf("stderr = %q, want the reason explained", e.stderr)
	}
}

func TestEnsureProxyFollowsUpstreamDrift(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
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
