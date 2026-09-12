package lifecycle_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
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
	return e.sandbox.RegisteredPaths(consent.ProjectIDHash(root))
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

// discardIO is the IO a detached reader runs with: nothing it prints is
// read, so a test hands it the same silence production does.
func discardIO() lifecycle.IO { return lifecycle.IO{Out: io.Discard, Err: io.Discard} }

// putSessionFile writes content at rel under the session files root and
// returns its path.
func (e *env) putSessionFile(rel, content string) string {
	e.t.Helper()
	path := filepath.Join(e.sessionFilesRoot(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return path
}

// registerFile registers path for root's project with a session
// subpath, standing in for what a hook records.
func (e *env) registerFile(root, path, subpath string) {
	e.t.Helper()
	e.sandbox.RegisterSessionFile(consent.ProjectIDHash(root), path, subpath)
}

// registeredFiles returns root's registered entries, cursors included.
func (e *env) registeredFiles(root string) []proxytest.RegisteredFile {
	e.t.Helper()
	return e.sandbox.RegisteredFiles(consent.ProjectIDHash(root))
}

// storedRecords lists the segments and snapshots in this device's spool.
func (e *env) storedRecords() []proxytest.Record {
	e.t.Helper()
	return e.sandbox.Records()
}

// injectWithProxy writes the injection shape that routes the project's
// traffic through the proxy, as an ordinary enable would.
func (e *env) injectWithProxy() {
	e.t.Helper()
	if err := claudesettings.InjectProject(e.settingsPath(), "http://127.0.0.1:41100/t/tok-proj", e.projectHooks()); err != nil {
		e.t.Fatal(err)
	}
}

// aProxylessTarget points ensure at a free port with no binary behind
// it, so a reader's ensure-on-exit probes, fails to spawn, and stays
// silent — the tests that only care about the spool never start a real
// resident process.
func (e *env) aProxylessTarget() {
	e.t.Helper()
	e.deps.ProxyAddr = freeAddr(e.t)
}

func TestReadSessionFiles_StoresSegmentsAndAdvancesTheCursor(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", `{"type":"assistant","message":{"id":"m1"}}`+"\n")
	e.registerFile(root, main, "")

	e.machine().ReadSessionFiles(e.project, discardIO())

	recs := e.storedRecords()
	if len(recs) != 1 || recs[0].Kind != "segment" {
		t.Fatalf("records = %+v, want exactly one segment", recs)
	}
	files := e.registeredFiles(root)
	if len(files) != 1 || files[0].Offset == 0 || files[0].NextSegment != 1 {
		t.Fatalf("cursor = %+v, want advanced past the segment", files)
	}

	e.machine().ReadSessionFiles(e.project, discardIO())
	if got := e.storedRecords(); len(got) != 1 {
		t.Fatalf("records after a second run over unchanged files = %d, want still 1", len(got))
	}
}

func TestReadSessionFiles_FullSpoolLeavesTheCursorAlone(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	// A quota of one byte refuses every record while dropping nothing.
	e.sandbox.SeedHandshake(proxytest.Handshake{SpoolQuotaBytes: 1})
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", `{"type":"assistant","message":{"id":"m1"}}`+"\n")
	e.registerFile(root, main, "")

	e.machine().ReadSessionFiles(e.project, discardIO())
	if got := e.storedRecords(); len(got) != 0 {
		t.Fatalf("records = %d, want none stored under a full spool", len(got))
	}
	if f := e.registeredFiles(root)[0]; f.Offset != 0 || f.NextSegment != 0 {
		t.Fatalf("cursor = %+v, want left where it was", f)
	}

	// Space returns; the same bytes are read once, not repeated or lost.
	e.sandbox.SeedHandshake(proxytest.Handshake{SpoolQuotaBytes: 1 << 20})
	e.machine().ReadSessionFiles(e.project, discardIO())
	if got := e.storedRecords(); len(got) != 1 {
		t.Fatalf("records after space returns = %d, want the one delayed segment", len(got))
	}
}

func TestReadSessionFiles_StopsReadingVanishedAndRelocatedFiles(t *testing.T) {
	root := "" // filled per case from the enabled project
	tests := []struct {
		name    string
		prepare func(e *env) string // returns the registered path
	}{
		{
			name: "a file that vanished",
			prepare: func(e *env) string {
				return filepath.Join(e.sessionFilesRoot(), "-work-sample", "gone.jsonl")
			},
		},
		{
			name: "a session that left the consented directory",
			prepare: func(e *env) string {
				return e.putSessionFile("-work-sample/moved.jsonl", `{"type":"relocated","relocatedCwd":"/elsewhere/entirely"}`+"\n")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.aProxylessTarget()
			e.enableProject()
			e.injectWithoutBaseURL()
			root = e.canonicalRoot()
			path := tt.prepare(e)
			e.registerFile(root, path, "")

			e.machine().ReadSessionFiles(e.project, discardIO())
			if got := e.registeredFiles(root); len(got) != 0 {
				t.Errorf("registry = %+v, want nothing left to read", got)
			}
		})
	}
}

func TestReadSessionFiles_KeepsReadingPastAFileThatFails(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	// A metadata file caught mid-write is not JSON yet: reading it fails.
	bad := e.putSessionFile("-work-sample/0f1e2d3c/subagents/agent-a1.meta.json", `{"broken`)
	good := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", `{"type":"assistant","message":{"id":"m1"}}`+"\n")
	e.registerFile(root, bad, "")
	e.registerFile(root, good, "")

	e.machine().ReadSessionFiles(e.project, discardIO())
	recs := e.storedRecords()
	if len(recs) != 1 || recs[0].Kind != "segment" {
		t.Fatalf("records = %+v, want the good file's segment despite the failing one", recs)
	}
	// The failing file keeps its entry: its cursor never advanced.
	var kept bool
	for _, f := range e.registeredFiles(root) {
		if f.Path == bad {
			kept = true
		}
	}
	if !kept {
		t.Error("the failing file's entry was dropped, want it kept for the next run")
	}
}

func TestReadSessionFiles_FillsCaptureFromTheShapeTheGrantRecords(t *testing.T) {
	tests := []struct {
		name          string
		shape         proxytest.Shape
		inject        func(e *env)
		wantInjection string
	}{
		{name: "proxy shape", shape: proxytest.WithProxy, inject: (*env).injectWithProxy, wantInjection: "proxy"},
		{name: "no-proxy shape", shape: proxytest.WithoutProxy, inject: (*env).injectWithoutBaseURL, wantInjection: "tail_only"},
		{name: "settings file edited into the other shape", shape: proxytest.WithoutProxy, inject: (*env).injectWithProxy, wantInjection: "tail_only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.aProxylessTarget()
			root := e.canonicalRoot()
			e.sandbox.GrantProject(proxytest.Grant{
				Token:         "tok-proj",
				ProjectIDHash: consent.ProjectIDHash(root),
				RootPath:      root,
				Upstream:      "https://api.anthropic.com",
				Shape:         tt.shape,
			})
			tt.inject(e)
			main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", `{"type":"assistant","message":{"id":"m1"}}`+"\n")
			e.registerFile(root, main, "service/api")

			e.machine().ReadSessionFiles(e.project, discardIO())
			recs := e.storedRecords()
			if len(recs) != 1 {
				t.Fatalf("records = %+v, want one segment", recs)
			}
			seg, err := envelope.ParseSegment(recs[0].Raw)
			if err != nil {
				t.Fatal(err)
			}
			if seg.Capture.Injection != tt.wantInjection {
				t.Errorf("injection = %q, want %q", seg.Capture.Injection, tt.wantInjection)
			}
			if seg.Capture.ProjectSubpath != "service/api" {
				t.Errorf("project subpath = %q, want the one registration recorded", seg.Capture.ProjectSubpath)
			}
			if seg.Capture.ProjectIDHash != consent.ProjectIDHash(root) {
				t.Errorf("project id hash = %q, want the enabled project's", seg.Capture.ProjectIDHash)
			}
		})
	}
}

func TestReadSessionFiles_UnenabledProjectDoesNothing(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	// No grant, no injection: nothing to read and nothing to record.
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", `{"type":"assistant","message":{"id":"m1"}}`+"\n")
	e.registerFile(root, main, "")

	e.machine().ReadSessionFiles(e.project, discardIO())
	if got := e.storedRecords(); len(got) != 0 {
		t.Errorf("records = %d, want nothing for a project that is not enabled", len(got))
	}
}

func TestReadSessionFiles_KilledReaderIsIdempotentOnRerun(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", `{"type":"assistant","message":{"id":"m1"}}`+"\n")
	e.registerFile(root, main, "")

	e.machine().ReadSessionFiles(e.project, discardIO())
	if got := e.storedRecords(); len(got) != 1 {
		t.Fatalf("records after the first run = %d, want 1", len(got))
	}

	// A reader killed after storing the segment but before it advanced
	// the cursor leaves the entry as it was: rewind it to that state.
	f := e.registeredFiles(root)[0]
	f.Offset, f.Size, f.Inode, f.NextSegment, f.MessageIDs = 0, 0, 0, 0, nil
	e.sandbox.PutRegisteredFile(consent.ProjectIDHash(root), f)

	e.machine().ReadSessionFiles(e.project, discardIO())
	if got := e.storedRecords(); len(got) != 1 {
		t.Fatalf("records after the rerun = %d, want the segment stored once, not twice", len(got))
	}
}

// isolateForSpawn re-homes the device onto one directory a spawned
// process can resolve for itself, so a proxy this test starts writes the
// same stores the machine reads. It returns that layout and arranges for
// whatever proxy comes up to be drained with the test.
func (e *env) isolateForSpawn() userdirs.Layout {
	e.t.Helper()
	layout := proxytest.ResolvableLayout(e.t, e.deps.Home, e.t.TempDir())
	e.deps.Layout = layout
	e.sandbox = proxytest.Open(e.t, layout)
	e.seedDeviceToken()
	e.deps.ExecPath = procbin.Self(e.t, "cli")
	e.deps.ProxyAddr = freeAddr(e.t)
	e.t.Setenv(cli.ProxyAddrEnv, e.deps.ProxyAddr)
	e.sandbox.PointAtService(e.service.URL())
	addr := e.deps.ProxyAddr
	e.t.Cleanup(func() {
		_ = proxylife.For(layout, e.deps.Version, e.deps.ExecPath, addr).StopGone()
	})
	return layout
}

func TestReadSessionFiles_BringsUpTheResidentProcess(t *testing.T) {
	tests := []struct {
		name   string
		inject func(e *env)
	}{
		{name: "proxy shape", inject: (*env).injectWithProxy},
		{name: "no-proxy shape", inject: (*env).injectWithoutBaseURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			layout := e.isolateForSpawn()
			e.enableProject()
			tt.inject(e)
			root := e.canonicalRoot()
			main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", `{"type":"assistant","message":{"id":"m1"}}`+"\n")
			e.registerFile(root, main, "")

			e.machine().ReadSessionFiles(e.project, discardIO())

			waitHealthy(t, e, e.deps.ProxyAddr)
			v := proxylife.For(layout, e.deps.Version, e.deps.ExecPath, e.deps.ProxyAddr).Observe()
			if v.Holder != proxylife.HolderOurs {
				t.Fatalf("resident process holder = %v, want a healthy proxy of ours", v.Holder)
			}
		})
	}
}

func TestHook_EndToEnd_RegisterReadStore(t *testing.T) {
	e := newEnv(t)
	e.isolateForSpawn()
	e.enableProject()
	e.injectWithoutBaseURL()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", `{"type":"assistant","message":{"id":"m1"}}`+"\n")

	registered, err := e.machine().RegisterSessionFile(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project})
	if err != nil || !registered {
		t.Fatalf("register = %v, %v", registered, err)
	}
	if err := e.machine().SpawnReader(e.project); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		recs := e.storedRecords()
		if len(recs) == 1 && recs[0].Kind == "segment" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the spawned reader never stored the segment; records = %+v", recs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
