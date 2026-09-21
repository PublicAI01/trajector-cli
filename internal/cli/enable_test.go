package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/follow/discover"
	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// earlierSessionFile writes a session file of this project's, as
// Claude Code would have left one before the project was enabled.
func earlierSessionFile(t *testing.T, e *clitest.Env, sid string) string {
	t.Helper()
	proxytest.RequireSessionSources(t)
	dir := filepath.Join(e.Home(), "claude", "projects", discover.Encode(e.ProjectRoot()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"id":"`+sid+`"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEnable_NoEarlierLeavesTheProjectsEarlierSessionsAlone(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		shape proxytest.Shape
	}{
		{name: "on its own", args: []string{"enable", "--no-earlier"}, shape: proxytest.WithProxy},
		{name: "after the shape flag", args: []string{"enable", "--no-proxy", "--no-earlier"}, shape: proxytest.WithoutProxy},
		{name: "before the shape flag", args: []string{"enable", "--no-earlier", "--no-proxy"}, shape: proxytest.WithoutProxy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := clitest.New(t)
			e.Paired()
			p := e.StartProxy()
			defer p.Stop()
			earlierSessionFile(t, e, "0f1e2d3c")

			got := e.InProjectInput("yes\n", tt.args...)
			if got.Exit != 0 {
				t.Fatalf("%v = exit %d\nstdout: %s\nstderr: %s", tt.args, got.Exit, got.Stdout, got.Stderr)
			}
			if !strings.Contains(got.Stdout, "Earlier session records skipped.") {
				t.Errorf("stdout does not say the earlier sessions were skipped:\n%s", got.Stdout)
			}
			if strings.Contains(got.Stdout, "will be collected once") {
				t.Errorf("stdout counts earlier sessions although they were skipped:\n%s", got.Stdout)
			}
			grant, ok := e.Sandbox().ActiveGrant(e.Project())
			if !ok || grant.Shape != tt.shape || !grant.EarlierSkipped {
				t.Errorf("grant = %+v, want the shape and the skipped earlier sessions recorded", grant)
			}
			if files := e.Sandbox().RegisteredPaths(e.ProjectHash()); len(files) != 0 {
				t.Errorf("registered = %v, want none", files)
			}

			const notice = "Earlier sessions were skipped at enable; run `trajector enable` again (with --no-proxy if you use it) to collect them."
			if status := e.InProject("status"); !strings.Contains(status.Stdout, notice) {
				t.Errorf("status does not say the earlier sessions were skipped:\n%s", status.Stdout)
			}
		})
	}
}

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
