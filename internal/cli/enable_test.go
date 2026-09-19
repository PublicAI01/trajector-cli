package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestEnable_NoProxyFlagInstallsHooksWithoutABaseURL(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := clitest.New(t)
	e.Paired()
	p := e.StartProxy()
	defer p.Stop()

	got := e.InProjectInput("yes\n", "enable", "--no-proxy")
	if got.Exit != 0 {
		t.Fatalf("enable --no-proxy = exit %d\nstdout: %s\nstderr: %s", got.Exit, got.Stdout, got.Stderr)
	}
	for _, line := range []string{
		"No earlier session records to collect.",
		"This project records from its session files only, so Remote Control stays available.",
		"This project now contributes data.",
	} {
		if !strings.Contains(got.Stdout, line) {
			t.Errorf("stdout lacks %q:\n%s", line, got.Stdout)
		}
	}
	if strings.Contains(got.Stdout, "Remote Control: inside this project") {
		t.Errorf("stdout carries the notice for the other shape:\n%s", got.Stdout)
	}

	settings := e.ProjectSettings()
	if url, injected := settings.InjectedBaseURL(); injected {
		t.Errorf("a base URL was injected: %q", url)
	}
	if shape, ok := settings.Shape(); !ok || shape != proxytest.WithoutProxy {
		t.Errorf("injection shape = %q, %v, want the shape that routes no traffic through the proxy", shape, ok)
	}
	for _, marker := range []string{proxytest.EnsureProxyMarker, proxytest.SessionEndMarker, proxytest.GitSnapshotMarker} {
		if !settings.HasHook(marker) {
			t.Errorf("no hook carrying %q:\n%s", marker, settings.Contents())
		}
	}
	if grant, ok := e.Sandbox().ActiveGrant(e.Project()); !ok || grant.Shape != proxytest.WithoutProxy {
		t.Errorf("grant = %+v, want the shape recorded on the grant", grant)
	}
}

func TestEnable_HooksThatWillNotLoadNameTheSettingsRankNotAFile(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	p := e.StartProxy()
	defer p.Stop()

	managed := filepath.Join(e.Home(), "managed", "managed-settings.json")
	if err := os.MkdirAll(filepath.Dir(managed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte(`{"disableAllHooks": true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := e.InProjectInput("yes\n", "enable")
	if got.Exit != 0 {
		t.Fatalf("enable = exit %d\nstdout: %s\nstderr: %s", got.Exit, got.Stdout, got.Stderr)
	}
	const opener = "Claude Code will not load trajector's hooks in this project ("
	_, after, found := strings.Cut(got.Stdout, opener)
	if !found {
		t.Fatalf("stdout does not say the hooks will not load:\n%s", got.Stdout)
	}
	reason, _, found := strings.Cut(after, ")")
	if !found {
		t.Fatalf("the reading of the hooks is not closed off:\n%s", got.Stdout)
	}
	if reason != "disableAllHooks in managed settings" {
		t.Errorf("reason = %q, want the setting and the rank that set it", reason)
	}
	if strings.ContainsAny(reason, `/\`) {
		t.Errorf("reason = %q, want no file path in it", reason)
	}
}
