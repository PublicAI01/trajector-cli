package cli_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
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
	proxytest.RequireSessionSources(t)
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

// A session hook brings the proxy up, in a process that outlives the
// hook. A suite that drives the CLI in its own process cannot stop
// such a process, so the environment records the start instead of
// performing it: the hook still reaches the start and still reports
// success, and the address it named stays free.
func TestHook_SessionStartRecordsTheProxyItWouldStartAndStartsNone(t *testing.T) {
	h := newHookEnv(t)
	h.enabled()

	assertSilentSuccess(t, h.InProjectInput(h.input(h.sessionFile("-work-sample/0f1e2d3c.jsonl")), "hook", "ensure-proxy"))

	if got, want := h.ProxyStarts(), []string{h.ProxyAddr()}; !slices.Equal(got, want) {
		t.Errorf("recorded proxy starts = %q, want %q", got, want)
	}
	if conn, err := net.DialTimeout("tcp", h.ProxyAddr(), 2*time.Second); err == nil {
		conn.Close()
		t.Errorf("something listens at %s, want the hook to have started no proxy", h.ProxyAddr())
	}
}

// A hook that reaches no proxy hands the reading to a process of its
// own, which outlives the hook exactly as the proxy does. The
// environment records that start as well, so the hook is seen to have
// reached it and the suite is left with no reader to stop.
func TestHook_SessionEndRecordsTheReaderItWouldStartAndStartsNone(t *testing.T) {
	h := newHookEnv(t)
	h.enabled()

	assertSilentSuccess(t, h.InProjectInput(h.input(h.sessionFile("-work-sample/0f1e2d3c.jsonl")), "hook", "session-end"))

	// The reader is started with the directory the hook ran in, spelled
	// as the shell spelled it; the project root is that directory with
	// its symbolic links resolved, which on macOS puts /private in front
	// of a temporary directory.
	got := h.Spawns()
	for i := range got {
		if resolved, err := filepath.EvalSymlinks(got[i].Target); err == nil {
			got[i].Target = resolved
		}
	}
	want := []proxylife.LoggedStart{{Command: "hook", Target: h.ProjectRoot()}}
	if !slices.Equal(got, want) {
		t.Errorf("recorded starts = %v, want %v", got, want)
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

func TestHook_TellsAStoppedDeviceOncePerSession(t *testing.T) {
	h := newHookEnv(t)
	h.enabled()
	main := h.sessionFile("-work-sample/0f1e2d3c.jsonl")
	// Registered by the first hook of the session, which runs before
	// the pause here so that the notice is the only thing that changes.
	assertSilentSuccess(t, h.InProjectInput(h.input(main), "hook", "session-end"))
	h.Sandbox().Pause(proxytest.PauseSignedOut)

	first := h.InProjectInput(h.input(main), "hook", "progress")
	if first.Exit != 1 {
		t.Errorf("exit = %d, want 1 so the session shows the line to the user (stderr: %q)", first.Exit, first.Stderr)
	}
	if !strings.Contains(first.Stderr, "nothing of this session is being recorded") ||
		!strings.Contains(first.Stderr, "trajector status") {
		t.Errorf("stderr = %q, want the line that sends the session to the command that says why", first.Stderr)
	}
	// A pause is one of the reasons the line covers, and the line names
	// none of them: a session told "paused" while a full spool stopped
	// it would be sent to repair the wrong thing.
	if strings.Contains(first.Stderr, "paused") {
		t.Errorf("stderr = %q, want no reason named in a line that stands for several", first.Stderr)
	}
	if first.Stdout != "" {
		t.Errorf("stdout = %q, want nothing said to the model", first.Stdout)
	}
	for _, hook := range []string{"progress", "session-end"} {
		again := h.InProjectInput(h.input(main), "hook", hook)
		if again.Exit != 0 || again.Stderr != "" {
			t.Errorf("%s after the session was told: exit = %d, stderr = %q; want one notice per session", hook, again.Exit, again.Stderr)
		}
	}
}

func TestHook_TellsASessionWhenTheSpoolWillTakeNoMore(t *testing.T) {
	h := newHookEnv(t)
	h.enabled()
	h.Sandbox().SeedHandshake(proxytest.Handshake{SpoolQuotaBytes: 1})
	h.Sandbox().SeedRawcall("req-1", h.ProjectHash(), time.Now())
	main := h.sessionFile("-work-sample/0f1e2d3c.jsonl")

	got := h.InProjectInput(h.input(main), "hook", "progress")
	if got.Exit != 1 {
		t.Errorf("exit = %d, want 1 so the session shows the line to the user (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stderr, "nothing of this session is being recorded") {
		t.Errorf("stderr = %q, want a full spool to reach the session as the same line a pause does", got.Stderr)
	}
}

func TestHook_TellsASessionWhenTheRoutingTableCannotBeRead(t *testing.T) {
	h := newHookEnv(t)
	h.enabled()
	main := h.sessionFile("-work-sample/0f1e2d3c.jsonl")
	assertSilentSuccess(t, h.InProjectInput(h.input(main), "hook", "session-end"))
	h.Sandbox().CorruptRoutingTable()

	got := h.InProjectInput(h.input(main), "hook", "progress")
	if got.Exit != 1 {
		t.Errorf("exit = %d, want 1 so the session shows the line to the user (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stderr, "nothing of this session is being recorded") {
		t.Errorf("stderr = %q, want an unreadable table to reach the session as the same line a pause does", got.Stderr)
	}
}

func TestHook_SaysNothingWhileTheDeviceRecords(t *testing.T) {
	h := newHookEnv(t)
	h.enabled()
	main := h.sessionFile("-work-sample/0f1e2d3c.jsonl")

	assertSilentSuccess(t, h.InProjectInput(h.input(main), "hook", "session-end"))
	assertSilentSuccess(t, h.InProjectInput(h.input(main), "hook", "progress"))
}
