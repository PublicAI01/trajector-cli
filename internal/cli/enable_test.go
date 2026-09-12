package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestEnable_NoProxyFlagInstallsHooksWithoutABaseURL(t *testing.T) {
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

	data, err := os.ReadFile(filepath.Join(e.Project(), ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Env   map[string]string `json:"env"`
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings: %v\n%s", err, data)
	}
	if _, ok := settings.Env["ANTHROPIC_BASE_URL"]; ok {
		t.Errorf("a base URL was injected:\n%s", data)
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "SessionEnd"} {
		if len(settings.Hooks[event]) == 0 {
			t.Errorf("%s hook missing:\n%s", event, data)
		}
	}
	if cmd := settings.Hooks["SessionStart"][0].Hooks[0].Command; !strings.HasSuffix(cmd, " hook ensure-proxy --no-proxy") {
		t.Errorf("SessionStart command = %q, want it marked --no-proxy", cmd)
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
