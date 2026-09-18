package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// hookEnv is a clitest environment with a Claude configuration
// directory of the test's own, where a process a hook starts is this
// binary's CLI.
type hookEnv struct {
	*clitest.Env
	t         *testing.T
	configDir string
}

func newHookEnv(t *testing.T) *hookEnv {
	t.Helper()
	e := clitest.New(t)
	procbin.Self(t, "cli")
	configDir := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	return &hookEnv{Env: e, t: t, configDir: configDir}
}

// enabled grants the project, as a completed enable would.
func (h *hookEnv) enabled() {
	h.t.Helper()
	h.Sandbox().GrantProject(proxytest.Grant{
		Token:         "tok-proj",
		ProjectIDHash: h.ProjectHash(),
		RootPath:      h.ProjectRoot(),
		Upstream:      "https://api.anthropic.com",
	})
}

// sessionFile creates an empty file at rel under the session files
// root and returns its path.
func (h *hookEnv) sessionFile(rel string) string {
	h.t.Helper()
	path := filepath.Join(h.configDir, "projects", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		h.t.Fatal(err)
	}
	return path
}

// input is the JSON a session writes on a hook's stdin for path.
func (h *hookEnv) input(path string) string {
	h.t.Helper()
	data, err := json.Marshal(map[string]string{
		"session_id":      "0f1e2d3c",
		"transcript_path": path,
		"cwd":             h.Project(),
		"hook_event_name": "SessionStart",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

func (h *hookEnv) registered() []string {
	h.t.Helper()
	return h.Sandbox().RegisteredPaths(h.ProjectHash())
}

func assertSilentSuccess(t *testing.T, got clitest.Result) {
	t.Helper()
	if got.Exit != 0 {
		t.Errorf("exit = %d, want 0 (stderr: %q)", got.Exit, got.Stderr)
	}
	if got.Stdout != "" || got.Stderr != "" {
		t.Errorf("stdout = %q, stderr = %q, want both empty", got.Stdout, got.Stderr)
	}
}

func TestHook_RegistersTheSessionFileItIsToldAbout(t *testing.T) {
	for _, hook := range []string{"ensure-proxy", "session-end"} {
		t.Run(hook, func(t *testing.T) {
			h := newHookEnv(t)
			h.StartProxy()
			h.enabled()
			main := h.sessionFile("-work-sample/0f1e2d3c.jsonl")
			agent := h.sessionFile("-work-sample/0f1e2d3c/subagents/agent-a1.jsonl")

			assertSilentSuccess(t, h.InProjectInput(h.input(main), "hook", hook))
			want := []string{main, agent}
			if got := h.registered(); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("registry = %q, want %q", got, want)
			}
		})
	}
}

func TestHook_CloudSessionPlaceholderIsNotRegistered(t *testing.T) {
	h := newHookEnv(t)
	h.enabled()
	path := h.sessionFile("-work-sample/cloud-transcript.jsonl")

	assertSilentSuccess(t, h.InProjectInput(h.input(path), "hook", "session-end"))
	if got := h.registered(); len(got) != 0 {
		t.Errorf("registry = %q, want nothing", got)
	}
}

func TestHook_PathOutsideProjectsIsNotRegistered(t *testing.T) {
	tests := []struct {
		name string
		path func(h *hookEnv) string
	}{
		{
			name: "beside the projects directory",
			path: func(h *hookEnv) string { return filepath.Join(h.configDir, "other", "0f1e2d3c.jsonl") },
		},
		{
			name: "climbing out with dot-dot",
			path: func(h *hookEnv) string {
				return filepath.Join(h.configDir, "projects", "-work-sample", "..", "..", "0f1e2d3c.jsonl")
			},
		},
		{
			name: "under the default configuration directory while CLAUDE_CONFIG_DIR points elsewhere",
			path: func(h *hookEnv) string {
				return filepath.Join(h.Home(), ".claude", "projects", "-work-sample", "0f1e2d3c.jsonl")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHookEnv(t)
			h.enabled()
			assertSilentSuccess(t, h.InProjectInput(h.input(tt.path(h)), "hook", "session-end"))
			if got := h.registered(); len(got) != 0 {
				t.Errorf("registry = %q, want nothing", got)
			}
		})
	}
}

func TestHook_ProjectNotEnabledRegistersNothing(t *testing.T) {
	h := newHookEnv(t)
	path := h.sessionFile("-work-sample/0f1e2d3c.jsonl")

	assertSilentSuccess(t, h.InProjectInput(h.input(path), "hook", "session-end"))
	if got := h.registered(); len(got) != 0 {
		t.Errorf("registry = %q, want nothing", got)
	}
	if _, err := os.Stat(h.Layout().FollowDir()); !os.IsNotExist(err) {
		t.Errorf("the registry directory exists for a project that was never enabled (stat: %v)", err)
	}
}

func TestHook_MalformedStdinStillEnsuresProxy(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty stdin", input: ""},
		{name: "not JSON", input: "hello\n"},
		{name: "JSON without a path", input: `{"session_id":"0f1e2d3c","cwd":"/work"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHookEnv(t)
			h.StartProxy()
			// A stale agreement is what ensure-proxy acts on without a
			// project: the pause it records proves the hook ran to its end.
			h.Sandbox().AcceptAgreement("2020-01-obsolete", "2020-01-01T00:00:00Z")

			got := h.InProjectInput(tt.input, "hook", "ensure-proxy")
			if got.Exit != 0 || got.Stdout != "" {
				t.Errorf("exit = %d, stdout = %q, want 0 and silence", got.Exit, got.Stdout)
			}
			if !strings.Contains(got.Stderr, "agreement changed") {
				t.Errorf("stderr = %q, want the pause explained", got.Stderr)
			}
			if reason := h.Sandbox().PausedReason(); reason != proxytest.PauseConsentReconfirm {
				t.Errorf("pause = %q, want the reconfirmation pause", reason)
			}
		})
	}
}

func TestHook_SessionEndRegistersWithinBudget(t *testing.T) {
	h := newHookEnv(t)
	h.enabled()
	path := h.sessionFile("-work-sample/0f1e2d3c.jsonl")

	start := time.Now()
	got := h.InProjectInput(h.input(path), "hook", "session-end")
	took := time.Since(start)
	assertSilentSuccess(t, got)
	if len(h.registered()) != 1 {
		t.Fatalf("registry = %q, want the session file", h.registered())
	}
	if took > 500*time.Millisecond {
		t.Errorf("session-end took %v, want well inside the budget a closing session allows", took)
	}
}

func TestHook_WindowsShapedPathIsNotRegistered(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "drive letter with backslashes", path: `C:\Users\me\.claude\projects\-work-sample\0f1e2d3c.jsonl`},
		{name: "drive letter with forward slashes", path: "C:/Users/me/.claude/projects/-work-sample/0f1e2d3c.jsonl"},
		{name: "UNC path", path: `\\wsl.localhost\Ubuntu\home\me\.claude\projects\-work-sample\0f1e2d3c.jsonl`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHookEnv(t)
			h.enabled()
			assertSilentSuccess(t, h.InProjectInput(h.input(tt.path), "hook", "session-end"))
			if got := h.registered(); len(got) != 0 {
				t.Errorf("registry = %q, want nothing", got)
			}
		})
	}
}

func TestHook_NoProxyArgumentIsAccepted(t *testing.T) {
	h := newHookEnv(t)
	h.StartProxy()
	h.enabled()
	path := h.sessionFile("-work-sample/0f1e2d3c.jsonl")

	assertSilentSuccess(t, h.InProjectInput(h.input(path), "hook", "ensure-proxy", "--no-proxy"))
	if got := h.registered(); len(got) != 1 || got[0] != path {
		t.Errorf("registry = %q, want %q", got, path)
	}
}

func TestHook_SubcommandsRejectStrayArguments(t *testing.T) {
	tests := [][]string{
		{"hook", "ensure-proxy", "extra"},
		{"hook", "ensure-proxy", "--no-proxy", "extra"},
		{"hook", "session-end", "extra"},
		{"hook", "git-snapshot", "extra"},
		{"hook", "discovery", "extra"},
		{"hook", "read"},
		{"hook", "read", "one", "two"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e := clitest.New(t)
			got := e.Run(args...)
			if got.Exit != 2 || !strings.Contains(got.Stderr, "usage: trajector hook") {
				t.Errorf("got %+v, want a usage error", got)
			}
		})
	}
}

func TestHook_ReadExitsSilently(t *testing.T) {
	e := clitest.New(t)
	assertSilentSuccess(t, e.Run("hook", "read", e.Project()))
}
