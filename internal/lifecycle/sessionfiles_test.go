package lifecycle_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/follow"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

// exitFileEnv names the file a spawned CLI writes its exit code and
// arguments to, since a detached process is released, never awaited.
const exitFileEnv = "TRAJECTOR_TEST_EXIT_FILE"

func TestMain(m *testing.M) {
	procbin.Main(m, map[string]func(args []string) int{
		"cli": func(args []string) int {
			exit := cli.Run(args, strings.NewReader(""), io.Discard, io.Discard)
			if path := os.Getenv(exitFileEnv); path != "" {
				record := fmt.Sprintf("%d\n%s", exit, strings.Join(args, " "))
				if err := os.WriteFile(path+".tmp", []byte(record), 0o600); err != nil {
					return 98
				}
				if err := os.Rename(path+".tmp", path); err != nil {
					return 98
				}
			}
			return exit
		},
	})
}

func (e *env) enableProject() {
	e.t.Helper()
	e.enableRoot(e.canonicalRoot())
}

func (e *env) enableRoot(root string) {
	e.t.Helper()
	e.sandbox.GrantProject(proxytest.Grant{
		Token:         "tok-proj",
		ProjectIDHash: consent.ProjectIDHash(root),
		RootPath:      root,
		Upstream:      "https://api.anthropic.com",
	})
}

// sessionFilesRoot is where the machine expects session files by
// default: projects/ under the user's Claude configuration directory.
func (e *env) sessionFilesRoot() string {
	return filepath.Join(e.deps.Home, ".claude", "projects")
}

func (e *env) registeredPaths(root string) []string {
	e.t.Helper()
	files, err := follow.Open(e.layout().FollowDir()).Files(consent.ProjectIDHash(root))
	if err != nil {
		e.t.Fatal(err)
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}

func TestRegisterSessionFile(t *testing.T) {
	const sessionFile = "-work-sample/0f1e2d3c.jsonl"
	tests := []struct {
		name string
		// enabled grants the project the hook runs in.
		enabled bool
		// configDir sets CLAUDE_CONFIG_DIR before the hook runs.
		configDir string
		// path is the session path the hook is told about; %s stands
		// for the default session files root, %c for the one under
		// configDir.
		path string
		want bool
	}{
		{
			name:    "session file under the projects directory",
			enabled: true,
			path:    "%s/" + sessionFile,
			want:    true,
		},
		{
			name:    "project not enabled",
			enabled: false,
			path:    "%s/" + sessionFile,
			want:    false,
		},
		{
			name:    "no path in the hook input",
			enabled: true,
			path:    "",
			want:    false,
		},
		{
			name:    "relative path",
			enabled: true,
			path:    "projects/" + sessionFile,
			want:    false,
		},
		{
			name:    "placeholder file of a cloud session",
			enabled: true,
			path:    "%s/-work-sample/cloud-transcript.jsonl",
			want:    false,
		},
		{
			name:    "file beside the projects directory",
			enabled: true,
			path:    "%s/../other/" + sessionFile,
			want:    false,
		},
		{
			name:    "the projects directory itself",
			enabled: true,
			path:    "%s",
			want:    false,
		},
		{
			name:    "dot-dot climbing out of the projects directory",
			enabled: true,
			path:    "%s/-work-sample/../../" + sessionFile,
			want:    false,
		},
		{
			name:      "CLAUDE_CONFIG_DIR relocates the projects directory",
			enabled:   true,
			configDir: "elsewhere",
			path:      "%c/" + sessionFile,
			want:      true,
		},
		{
			name:      "default projects directory is not consulted once CLAUDE_CONFIG_DIR is set",
			enabled:   true,
			configDir: "elsewhere",
			path:      "%s/" + sessionFile,
			want:      false,
		},
		{
			name:    "drive letter with backslashes",
			enabled: true,
			path:    `C:\Users\me\.claude\projects\-work-sample\0f1e2d3c.jsonl`,
			want:    false,
		},
		{
			name:    "drive letter with forward slashes",
			enabled: true,
			path:    "C:/Users/me/.claude/projects/-work-sample/0f1e2d3c.jsonl",
			want:    false,
		},
		{
			name:    "backslash inside an otherwise absolute path",
			enabled: true,
			path:    `%s\-work-sample\0f1e2d3c.jsonl`,
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if tt.enabled {
				e.enableProject()
			}
			configRoot := ""
			if tt.configDir != "" {
				dir := filepath.Join(t.TempDir(), tt.configDir)
				e.environ["CLAUDE_CONFIG_DIR"] = dir
				configRoot = filepath.Join(dir, "projects")
			}
			path := strings.NewReplacer("%s", e.sessionFilesRoot(), "%c", configRoot).Replace(tt.path)

			got, err := e.machine().RegisterSessionFile(e.project, lifecycle.HookInput{SessionPath: path})
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("registered = %v, want %v", got, tt.want)
			}
			registered := e.registeredPaths(e.canonicalRoot())
			if tt.want && (len(registered) != 1 || registered[0] != filepath.Clean(path)) {
				t.Errorf("registry = %q, want exactly %q", registered, filepath.Clean(path))
			}
			if !tt.want && len(registered) != 0 {
				t.Errorf("registry = %q, want nothing registered", registered)
			}
		})
	}
}

func TestRegisterSessionFileTakesTheAgentFilesBesideIt(t *testing.T) {
	e := newEnv(t)
	e.enableProject()
	sessionDir := filepath.Join(e.sessionFilesRoot(), "-work-sample")
	main := filepath.Join(sessionDir, "0f1e2d3c.jsonl")
	agents := filepath.Join(sessionDir, "0f1e2d3c", "subagents")
	if err := os.MkdirAll(agents, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0f1e2d3c.jsonl", "0f1e2d3c/subagents/agent-a1.jsonl", "0f1e2d3c/subagents/agent-a1.meta.json"} {
		if err := os.WriteFile(filepath.Join(sessionDir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for range 2 {
		registered, err := e.machine().RegisterSessionFile(e.project, lifecycle.HookInput{SessionPath: main})
		if err != nil || !registered {
			t.Fatalf("registered = %v, %v", registered, err)
		}
	}
	want := []string{
		main,
		filepath.Join(agents, "agent-a1.jsonl"),
		filepath.Join(agents, "agent-a1.meta.json"),
	}
	if got := e.registeredPaths(e.canonicalRoot()); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("registry after two hooks = %q, want %q once each", got, want)
	}
}

func TestRegisterSessionFileLeavesAProjectUnderTheWSLMountAlone(t *testing.T) {
	e := newEnv(t)
	root := "/mnt/c/work/sample"
	e.enableRoot(root)
	path := filepath.Join(e.sessionFilesRoot(), "-mnt-c-work-sample", "0f1e2d3c.jsonl")

	registered, err := e.machine().RegisterSessionFile(root, lifecycle.HookInput{SessionPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if registered {
		t.Error("a project under the WSL mount root was registered")
	}
	if got := e.registeredPaths(root); len(got) != 0 {
		t.Errorf("registry = %q, want nothing", got)
	}
}

func TestSpawnReaderStartsAProcessThatExitsCleanly(t *testing.T) {
	e := newEnv(t)
	userdirs.Isolate(t.Setenv, e.deps.Home)
	e.deps.ExecPath = procbin.Self(t, "cli")
	exitFile := filepath.Join(t.TempDir(), "exit")
	t.Setenv(exitFileEnv, exitFile)

	if err := e.machine().SpawnReader(e.project); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		record, err := os.ReadFile(exitFile)
		if err == nil {
			want := "0\nhook read " + e.project
			if string(record) != want {
				t.Errorf("spawned process recorded %q, want %q", record, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the spawned reader never recorded an exit")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSpawnReaderReportsAnExecutableItCannotStart(t *testing.T) {
	e := newEnv(t)
	if err := e.machine().SpawnReader(e.project); err == nil {
		t.Error("spawning a missing executable reported no error")
	}
}

func TestReadHookInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  lifecycle.HookInput
	}{
		{
			name:  "the three base fields",
			input: `{"session_id":"s1","transcript_path":"/x/s1.jsonl","cwd":"/work","hook_event_name":"SessionStart"}`,
			want:  lifecycle.HookInput{SessionID: "s1", SessionPath: "/x/s1.jsonl", Cwd: "/work"},
		},
		{name: "empty input", input: ""},
		{name: "not JSON", input: "hello\n"},
		{name: "a JSON value that is not an object", input: `"s1"`},
		{name: "an object without a path", input: `{"session_id":"s1"}`, want: lifecycle.HookInput{SessionID: "s1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lifecycle.ReadHookInput(strings.NewReader(tt.input)); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
