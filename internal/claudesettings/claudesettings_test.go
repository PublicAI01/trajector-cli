package claudesettings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
)

const (
	testBaseURL = "http://127.0.0.1:41100/t/tok-abc123"
	testHookCmd = `"/usr/local/bin/trajector" hook ensure-proxy`
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("settings file is not valid JSON: %v\n%s", err, data)
	}
	return root
}

func TestInjectProjectCreatesFileWithEnvAndBothHooks(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	if err := InjectProject(path, testBaseURL, testHookCmd); err != nil {
		t.Fatal(err)
	}

	settings := readJSON(t, path)
	env := settings["env"].(map[string]any)
	if env[envBaseURL] != testBaseURL {
		t.Errorf("env = %v", env)
	}
	hooks := settings["hooks"].(map[string]any)
	for _, event := range []string{eventSessionStart, eventUserPromptSubmit} {
		if !HasHook(path, EnsureProxyMarker) {
			t.Fatalf("missing ensure-proxy hook for %s", event)
		}
		if _, ok := hooks[event]; !ok {
			t.Errorf("missing hooks.%s", event)
		}
	}

	// Windows has no owner-only bit to assert: Chmod there toggles the
	// read-only attribute and nothing else, so the file this test wrote
	// reports 0666 however it was created. The token it embeds is
	// protected by the profile directory's ACL instead.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("created settings file mode = %v, want owner-only (embeds the token)", perm)
		}
	}
}

func TestInjectProjectPreservesUserContent(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	original := `{
		"permissions": {"allow": ["Bash(npm test)"]},
		"env": {"MY_VAR": "keep-me"},
		"hooks": {
			"SessionStart": [
				{"hooks": [{"type": "command", "command": "echo user-hook"}]}
			],
			"PostToolUse": [
				{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo tool"}]}
			]
		}
	}`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InjectProject(path, testBaseURL, testHookCmd); err != nil {
		t.Fatal(err)
	}

	settings := readJSON(t, path)
	env := settings["env"].(map[string]any)
	if env["MY_VAR"] != "keep-me" || env[envBaseURL] != testBaseURL {
		t.Errorf("env = %v", env)
	}
	if _, ok := settings["permissions"]; !ok {
		t.Error("permissions block lost")
	}
	hooks := settings["hooks"].(map[string]any)
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Error("user PostToolUse hook lost")
	}
	starts := hooks["SessionStart"].([]any)
	if len(starts) != 2 {
		t.Errorf("SessionStart groups = %d, want user's plus ours", len(starts))
	}
}

func TestInjectProjectIsIdempotent(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	for i := 0; i < 3; i++ {
		if err := InjectProject(path, testBaseURL, testHookCmd); err != nil {
			t.Fatal(err)
		}
	}
	settings := readJSON(t, path)
	hooks := settings["hooks"].(map[string]any)
	if starts := hooks[eventSessionStart].([]any); len(starts) != 1 {
		t.Errorf("SessionStart groups after repeat injection = %d, want 1", len(starts))
	}
}

func TestRemoveProjectRestoresOriginalShape(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	original := map[string]any{
		"permissions": map[string]any{"allow": []any{"Bash(npm test)"}},
		"env":         map[string]any{"MY_VAR": "keep-me"},
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo user-hook"}}},
			},
		},
	}
	data, _ := json.MarshalIndent(original, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InjectProject(path, testBaseURL, testHookCmd); err != nil {
		t.Fatal(err)
	}
	if err := RemoveProject(path); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(readJSON(t, path), original) {
		t.Errorf("settings after inject+remove differ from original:\n%v", readJSON(t, path))
	}
}

func TestRemoveProjectOnPureInjectionLeavesEmptyObject(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	if err := InjectProject(path, testBaseURL, testHookCmd); err != nil {
		t.Fatal(err)
	}
	if err := RemoveProject(path); err != nil {
		t.Fatal(err)
	}
	if settings := readJSON(t, path); len(settings) != 0 {
		t.Errorf("settings after removal = %v, want empty", settings)
	}
}

func TestRemoveProjectKeepsForeignBaseURL(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"env": {"ANTHROPIC_BASE_URL": "https://relay.example.com"}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveProject(path); err != nil {
		t.Fatal(err)
	}
	env := readJSON(t, path)["env"].(map[string]any)
	if env[envBaseURL] != "https://relay.example.com" {
		t.Errorf("user's own base URL removed: %v", env)
	}
}

func TestRemoveProjectMissingFileIsNoop(t *testing.T) {
	if err := RemoveProject(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Errorf("RemoveProject on missing file = %v", err)
	}
}

func TestInjectRefusesMalformedSections(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"env": "not-an-object"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InjectProject(path, testBaseURL, testHookCmd); err == nil {
		t.Error("injection over a malformed env block did not fail")
	}
}

func TestMalformedHookSettingsHandledConsistently(t *testing.T) {
	cmd := `"/usr/local/bin/trajector" hook discovery`
	tests := []struct {
		name           string
		content        string
		wantHas        bool
		wantInjectErr  bool
		afterRoundTrip string
	}{
		{
			name:          "hooks section is not an object",
			content:       `{"hooks": "bogus"}`,
			wantInjectErr: true,
		},
		{
			name:          "event groups value is not a list",
			content:       `{"hooks": {"SessionStart": "bogus"}}`,
			wantInjectErr: true,
		},
		{
			name:    "other event malformed while target event is free",
			content: `{"hooks": {"PostToolUse": "bogus"}}`,
		},
		{
			name:    "group is not a map",
			content: `{"hooks": {"SessionStart": ["bogus"]}}`,
		},
		{
			name:    "group hooks value is not a list",
			content: `{"hooks": {"SessionStart": [{"hooks": "bogus"}]}}`,
		},
		{
			name:    "entry is not a map",
			content: `{"hooks": {"SessionStart": [{"hooks": ["bogus"]}]}}`,
		},
		{
			name:    "entry has no command",
			content: `{"hooks": {"SessionStart": [{"hooks": [{"type": "command"}]}]}}`,
		},
		{
			name:    "foreign entry among malformed neighbors",
			content: `{"hooks": {"SessionStart": ["bogus", {"hooks": [{"type": "command", "command": "echo user-hook"}]}]}}`,
		},
		{
			name:           "hooks object is empty",
			content:        `{"hooks": {}}`,
			afterRoundTrip: `{}`,
		},
		{
			name:           "event groups list is empty",
			content:        `{"hooks": {"SessionStart": []}}`,
			afterRoundTrip: `{}`,
		},
		{
			name:    "group hooks list is empty",
			content: `{"hooks": {"SessionStart": [{"hooks": []}]}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			original := readJSON(t, path)

			if got := HasHook(path, DiscoveryMarker); got != tt.wantHas {
				t.Errorf("HasHook = %v, want %v", got, tt.wantHas)
			}

			for i := 0; i < 2; i++ {
				if err := RemoveUserHook(path); err != nil {
					t.Fatalf("RemoveUserHook #%d: %v", i+1, err)
				}
				if got := readJSON(t, path); !reflect.DeepEqual(got, original) {
					t.Fatalf("RemoveUserHook #%d rewrote foreign content:\n%v", i+1, got)
				}
			}

			err := InjectUserHook(path, cmd)
			if tt.wantInjectErr {
				if err == nil {
					t.Fatal("InjectUserHook over a malformed section did not fail")
				}
				if got := readJSON(t, path); !reflect.DeepEqual(got, original) {
					t.Fatalf("failed injection changed the file:\n%v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !HasHook(path, DiscoveryMarker) {
				t.Fatal("hook not reported after injection")
			}

			if err := RemoveUserHook(path); err != nil {
				t.Fatal(err)
			}
			want := original
			if tt.afterRoundTrip != "" {
				want = map[string]any{}
				if err := json.Unmarshal([]byte(tt.afterRoundTrip), &want); err != nil {
					t.Fatal(err)
				}
			}
			if got := readJSON(t, path); !reflect.DeepEqual(got, want) {
				t.Errorf("inject+remove round trip = %v, want %v", got, want)
			}
		})
	}
}

func TestInjectedBaseURLAndTokenRoundTrip(t *testing.T) {
	root := t.TempDir()
	path := ProjectLocalPath(root)
	if _, ok := InjectedBaseURL(path); ok {
		t.Error("injected URL reported before injection")
	}
	if err := InjectProject(path, testBaseURL, testHookCmd); err != nil {
		t.Fatal(err)
	}
	url, ok := InjectedBaseURL(path)
	if !ok || url != testBaseURL {
		t.Fatalf("InjectedBaseURL = %q, %v", url, ok)
	}
	token, ok := TokenFromBaseURL(url)
	if !ok || token != "tok-abc123" {
		t.Errorf("TokenFromBaseURL = %q, %v", token, ok)
	}
}

func TestIsProxyBaseURLStaysNarrow(t *testing.T) {
	for value, want := range map[string]bool{
		"http://127.0.0.1:41100/t/tok":    true,
		"http://localhost:41100/t/tok":    true,
		"https://relay.example.com":       false,
		"https://relay.example.com/t/tok": false,
		"http://127.0.0.1:41100/":         false,
		"http://evil.example.com/t/tok":   false,
	} {
		if got := isProxyBaseURL(value); got != want {
			t.Errorf("isProxyBaseURL(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestUserHookInjectAndRemove(t *testing.T) {
	home := t.TempDir()
	path := UserSettingsPath(home)
	cmd := `"/usr/local/bin/trajector" hook discovery`
	if err := InjectUserHook(path, cmd); err != nil {
		t.Fatal(err)
	}
	if !HasHook(path, DiscoveryMarker) {
		t.Fatal("discovery hook not present after injection")
	}
	if err := RemoveUserHook(path); err != nil {
		t.Fatal(err)
	}
	if HasHook(path, DiscoveryMarker) {
		t.Error("discovery hook still present after removal")
	}
}

func TestConcurrentEditsLoseNoInjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = InjectUserHook(path, fmt.Sprintf("/usr/local/bin/trajector-%d hook discovery", i))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("inject %d: %v", i, err)
		}
	}
	for i := range n {
		marker := fmt.Sprintf("trajector-%d hook discovery", i)
		if !HasHook(path, marker) {
			t.Errorf("hook %q lost to a concurrent edit", marker)
		}
	}
}

func TestEffectiveEnvPrecedence(t *testing.T) {
	writeSettings := func(t *testing.T, path, key, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		content, _ := json.Marshal(map[string]any{"env": map[string]any{key: value}})
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	shell := func(value string) func(string) string {
		return func(key string) string {
			if key == envBaseURL {
				return value
			}
			return ""
		}
	}

	tests := []struct {
		name       string
		setup      func(t *testing.T, project, home string)
		getenv     func(string) string
		wantValue  string
		wantSource Source
		wantOK     bool
	}{
		{
			name:   "nothing configured",
			setup:  func(t *testing.T, project, home string) {},
			getenv: shell(""),
			wantOK: false,
		},
		{
			name:       "shell env only",
			setup:      func(t *testing.T, project, home string) {},
			getenv:     shell("https://relay.example.com"),
			wantValue:  "https://relay.example.com",
			wantSource: SourceShell,
			wantOK:     true,
		},
		{
			name: "user settings beat shell",
			setup: func(t *testing.T, project, home string) {
				writeSettings(t, UserSettingsPath(home), envBaseURL, "https://user.example.com")
			},
			getenv:     shell("https://shell.example.com"),
			wantValue:  "https://user.example.com",
			wantSource: SourceUser,
			wantOK:     true,
		},
		{
			name: "project settings beat user settings",
			setup: func(t *testing.T, project, home string) {
				writeSettings(t, UserSettingsPath(home), envBaseURL, "https://user.example.com")
				writeSettings(t, filepath.Join(project, ".claude", "settings.json"), envBaseURL, "https://project.example.com")
			},
			getenv:     shell(""),
			wantValue:  "https://project.example.com",
			wantSource: SourceProject,
			wantOK:     true,
		},
		{
			name: "project local beats everything",
			setup: func(t *testing.T, project, home string) {
				writeSettings(t, filepath.Join(project, ".claude", "settings.json"), envBaseURL, "https://project.example.com")
				writeSettings(t, ProjectLocalPath(project), envBaseURL, "https://local.example.com")
			},
			getenv:     shell(""),
			wantValue:  "https://local.example.com",
			wantSource: SourceProjectLocal,
			wantOK:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project, home := t.TempDir(), t.TempDir()
			tt.setup(t, project, home)
			value, source, ok := effectiveEnv(project, home, envBaseURL, tt.getenv)
			if ok != tt.wantOK || value != tt.wantValue || source != tt.wantSource {
				t.Errorf("effectiveEnv = %q, %q, %v; want %q, %q, %v", value, source, ok, tt.wantValue, tt.wantSource, tt.wantOK)
			}
		})
	}
}

func TestExternalBaseURLSkipsOwnInjection(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	if err := InjectProject(ProjectLocalPath(project), testBaseURL, testHookCmd); err != nil {
		t.Fatal(err)
	}
	getenv := func(key string) string {
		if key == envBaseURL {
			return "https://relay.example.com"
		}
		return ""
	}
	value, source, resolution := ExternalBaseURL(project, home, getenv)
	if resolution != BaseURLExternal || value != "https://relay.example.com" || source != SourceShell {
		t.Errorf("ExternalBaseURL = %q, %q, %v", value, source, resolution)
	}

	noShell := func(string) string { return "" }
	if _, _, resolution := ExternalBaseURL(project, home, noShell); resolution != BaseURLNone {
		t.Errorf("own injection reported as %v, want nothing configured", resolution)
	}
}

func TestOurOwnInjectionInTheEnvironmentHidesTheAnswerRatherThanBeingOne(t *testing.T) {
	// Claude Code applies the settings env block to everything it
	// spawns, so a hook it runs sees our injected base URL in place of
	// whatever the user's shell exported. The chain cannot see the
	// user's relay from there — and must say so, because answering
	// "nothing configured" is how a relay user's traffic ends up at the
	// official endpoint carrying credentials that endpoint will reject.
	project, home := t.TempDir(), t.TempDir()
	if err := InjectProject(ProjectLocalPath(project), testBaseURL, testHookCmd); err != nil {
		t.Fatal(err)
	}
	inSession := func(key string) string {
		if key == envBaseURL {
			return testBaseURL
		}
		return ""
	}
	if _, _, resolution := ExternalBaseURL(project, home, inSession); resolution != BaseURLMasked {
		t.Errorf("ExternalBaseURL = %v, want the answer reported as hidden", resolution)
	}

	// A relay the user put in the project's shared settings is still
	// visible from inside a session: only the shell layer is masked.
	if err := os.WriteFile(projectSharedPath(project), []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://relay.example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	value, source, resolution := ExternalBaseURL(project, home, inSession)
	if resolution != BaseURLExternal || value != "https://relay.example.com" || source != SourceProject {
		t.Errorf("ExternalBaseURL = %q, %q, %v; want the relay in the shared settings", value, source, resolution)
	}
}

func TestUnsupportedChannelDetection(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	getenv := func(key string) string {
		if key == "CLAUDE_CODE_USE_BEDROCK" {
			return "1"
		}
		return ""
	}
	key, found := UnsupportedChannel(project, home, getenv)
	if !found || key != "CLAUDE_CODE_USE_BEDROCK" {
		t.Errorf("UnsupportedChannel = %q, %v", key, found)
	}
	off := func(key string) string {
		if key == "CLAUDE_CODE_USE_BEDROCK" {
			return "0"
		}
		return ""
	}
	if _, found := UnsupportedChannel(project, home, off); found {
		t.Error("disabled channel flag reported as unsupported")
	}
}

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	gitAvailable(t)
	// Isolate from the developer's global git configuration: a global
	// excludes file covering .claude/ would change what check-ignore
	// reports.
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(isolated, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return root
}

func TestEnsureGitIgnoredAppendsWhenUncovered(t *testing.T) {
	root := initRepo(t)
	action, err := EnsureGitIgnored(root, ".claude/settings.local.json")
	if err != nil {
		t.Fatal(err)
	}
	if action != IgnoreAppended {
		t.Errorf("action = %q, want appended", action)
	}
	again, err := EnsureGitIgnored(root, ".claude/settings.local.json")
	if err != nil {
		t.Fatal(err)
	}
	if again != IgnoreCovered {
		t.Errorf("second run action = %q, want covered", again)
	}
}

func TestEnsureGitIgnoredPreservesDirectoryOnlyRules(t *testing.T) {
	root := initRepo(t)
	action, err := EnsureGitIgnored(root, "dist-*/")
	if err != nil {
		t.Fatal(err)
	}
	if action != IgnoreAppended {
		t.Errorf("action = %q, want appended", action)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "dist-*/\n" {
		t.Errorf(".gitignore = %q, want the trailing slash kept", data)
	}
	again, err := EnsureGitIgnored(root, "dist-*/")
	if err != nil {
		t.Fatal(err)
	}
	if again != IgnoreCovered {
		t.Errorf("second run action = %q, want covered", again)
	}
}

func TestEnsureGitIgnoredRespectsExistingCoverage(t *testing.T) {
	root := initRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".claude/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	action, err := EnsureGitIgnored(root, ".claude/settings.local.json")
	if err != nil {
		t.Fatal(err)
	}
	if action != IgnoreCovered {
		t.Errorf("action = %q, want covered", action)
	}
}

func TestEnsureGitIgnoredRefusesASymlinkedGitignore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privilege on windows")
	}
	root := initRepo(t)
	target := filepath.Join(t.TempDir(), "victim")
	before := []byte("victim content\n")
	if err := os.WriteFile(target, before, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".gitignore")); err != nil {
		t.Fatal(err)
	}

	action, err := EnsureGitIgnored(root, ".claude/settings.local.json")
	if err != nil {
		t.Fatal(err)
	}
	if action != IgnoreSymlinked {
		t.Errorf("action = %q, want symlinked", action)
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(after, before) {
		t.Errorf("link target = %q, %v, want it untouched", after, err)
	}
}

func TestEnsureGitIgnoredSkipsOutsideRepo(t *testing.T) {
	gitAvailable(t)
	action, err := EnsureGitIgnored(t.TempDir(), ".claude/settings.local.json")
	if err != nil {
		t.Fatal(err)
	}
	if action != IgnoreSkipped {
		t.Errorf("action = %q, want skipped", action)
	}
}

// Injection spells the base URL from whatever address the proxy is
// serving, and the proxy may serve any loopback address. Recognizing
// only 127.0.0.1 left an injection on any other one unrecognized, so
// removal walked past it and left the user's sessions pointing at a
// port that would soon be dead, with no command able to clear it.
// 2026-08-15.
func TestRemoveProjectClearsAnInjectionOnAnyLoopbackAddress(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:41100", "127.0.0.2:41100", "127.9.9.9:8080", "localhost:41100", "[::1]:41100"} {
		t.Run(addr, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			url := "http://" + addr + "/t/tok-abc123"
			if token, ok := TokenFromBaseURL(url); !ok || token != "tok-abc123" {
				t.Fatalf("TokenFromBaseURL(%q) = %q, %v; an injection it cannot read is one it cannot remove", url, token, ok)
			}
			if err := InjectProject(path, url, testHookCmd); err != nil {
				t.Fatal(err)
			}
			if err := RemoveProject(path); err != nil {
				t.Fatal(err)
			}
			if root := readJSON(t, path); root["env"] != nil {
				t.Errorf("removal left the injected base URL behind: %v", root)
			}
		})
	}
}

// The recognizer stays narrow in the other direction: removal must
// never mistake a base URL the user configured themselves for ours.
func TestTokenFromBaseURLRefusesWhatTrajectorDidNotInject(t *testing.T) {
	for _, url := range []string{
		"http://relay.example.com:8080/t/tok",
		"http://10.0.0.5:41100/t/tok",
		"http://127.0.0.1.evil.example:41100/t/tok",
		"http://0.0.0.0:41100/t/tok",
		"https://127.0.0.1:41100/t/tok",
	} {
		if token, ok := TokenFromBaseURL(url); ok {
			t.Errorf("TokenFromBaseURL(%q) = %q, want it unrecognized", url, token)
		}
	}
}

// enable appends three ignore rules and a diagnostic bundle two, from
// separate short-lived processes. The truncating write this used to do
// read the file before a concurrent append had landed and then replaced
// it, so one of the two was erased — and the rule that can go is the one
// keeping the injected settings file, which carries a consent token, out
// of the repository. 2026-08-15.
func TestEnsureGitIgnoredDoesNotClobberAConcurrentAppend(t *testing.T) {
	root := initRepo(t)
	path := filepath.Join(root, ".gitignore")

	holding := make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- fsatomic.Update(path, 0o644, func(old []byte) ([]byte, error) {
			close(holding)
			// Long enough that an unsynchronized appender reads the file
			// before this write lands, which is the race being pinned.
			time.Sleep(750 * time.Millisecond)
			return append(append([]byte{}, old...), []byte("held-rule\n")...), nil
		})
	}()
	<-holding

	action, err := EnsureGitIgnored(root, ".claude/settings.local.json")
	if err != nil {
		t.Fatal(err)
	}
	if action != IgnoreAppended {
		t.Fatalf("action = %q, want appended", action)
	}
	if err := <-held; err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"held-rule", ".claude/settings.local.json"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf(".gitignore = %q, want it to still carry %q", data, want)
		}
	}
}

// The user owns this file, so an append must not quietly widen it.
func TestEnsureGitIgnoredKeepsTheExistingFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Chmod only toggles the read-only bit on windows")
	}
	root := initRepo(t)
	path := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(path, []byte("build/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureGitIgnored(root, ".claude/settings.local.json"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want the user's own 0600 kept", info.Mode().Perm())
	}
}
