package lifecycle_test

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

func TestMain(m *testing.M) { proxytest.Main(m) }

func (e *env) enableProject() {
	e.t.Helper()
	e.enableRoot(e.canonicalRoot())
}

func (e *env) enableRoot(root string) {
	e.t.Helper()
	e.sandbox.GrantProject(proxytest.Grant{
		Token:         "tok-proj",
		ProjectIDHash: proxytest.ProjectIDHash(root),
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
	return e.sandbox.RegisteredPaths(proxytest.ProjectIDHash(root))
}

func TestSessionEndedRegistersTheSessionFileItWasToldAbout(t *testing.T) {
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

			e.machine().SessionEnded(e.project, lifecycle.HookInput{SessionPath: path})

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

func TestSessionEndedTakesTheAgentFilesBesideTheSessionFile(t *testing.T) {
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
		e.machine().SessionEnded(e.project, lifecycle.HookInput{SessionPath: main})
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

func TestSessionEndedLeavesAProjectUnderTheWSLMountAlone(t *testing.T) {
	e := newEnv(t)
	root := "/mnt/c/work/sample"
	e.enableRoot(root)
	path := filepath.Join(e.sessionFilesRoot(), "-mnt-c-work-sample", "0f1e2d3c.jsonl")

	e.machine().SessionEnded(root, lifecycle.HookInput{SessionPath: path})

	if got := e.registeredPaths(root); len(got) != 0 {
		t.Errorf("registry = %q, want nothing", got)
	}
}

func TestSessionStartingFollowsTheSessionAndObservesTheRepository(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.gitProject()
	e.gitCommit("main.go", "package main\n")
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", "")

	hook := lifecycle.HookInput{
		SessionID:   "0a1b2c3d-1111-4aaa-8aaa-000000000001",
		SessionPath: main,
		Cwd:         e.project,
		HookEvent:   "SessionStart",
	}
	if err := e.machine().SessionStarting(e.project, hook, e.io()); err != nil {
		t.Fatalf("session start: %v", err)
	}
	if got := e.registeredPaths(e.canonicalRoot()); len(got) != 1 || got[0] != main {
		t.Errorf("registry = %q, want %q", got, main)
	}
	if got := e.observations(); len(got) != 1 {
		t.Fatalf("observations = %+v, want the session opening observed once", got)
	}
}

func TestSessionEndedStartsAReaderThatExitsCleanly(t *testing.T) {
	e := newEnv(t)
	trajector := proxytest.InstalledTrajector(t, e.deps.Home)
	e.deps.ExecPath = trajector.Path()
	e.enableProject()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", "")

	e.machine().SessionEnded(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project})

	args, exit := trajector.AwaitRun(10 * time.Second)
	if want := "hook read " + e.project; args != want || exit != 0 {
		t.Errorf("the spawned reader ran %q and exited %d, want %q and 0", args, exit, want)
	}
}

func TestSessionEndedKeepsTheRegistrationWhenTheReaderCannotStart(t *testing.T) {
	e := newEnv(t)
	e.enableProject()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", "")

	e.machine().SessionEnded(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project})

	if got := e.registeredPaths(e.canonicalRoot()); len(got) != 1 || got[0] != main {
		t.Errorf("registry = %q, want %q", got, main)
	}
}

func TestReadHookInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  lifecycle.HookInput
	}{
		{
			name:  "the named fields",
			input: `{"session_id":"s1","transcript_path":"/x/s1.jsonl","cwd":"/work","hook_event_name":"SessionStart"}`,
			want:  lifecycle.HookInput{SessionID: "s1", SessionPath: "/x/s1.jsonl", Cwd: "/work", HookEvent: "SessionStart"},
		},
		{name: "empty input", input: ""},
		{name: "not JSON", input: "hello\n"},
		{name: "a JSON value that is not an object", input: `"s1"`},
		{name: "an object without a path", input: `{"session_id":"s1"}`, want: lifecycle.HookInput{SessionID: "s1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lifecycle.ReadHookInput(strings.NewReader(tt.input))
			if got.SessionID != tt.want.SessionID || got.SessionPath != tt.want.SessionPath ||
				got.Cwd != tt.want.Cwd || got.HookEvent != tt.want.HookEvent {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestHookInputReadsWhatAToolWasGivenAndAnswered(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantCommand string
		wantCommit  bool
	}{
		{
			name:        "a shell tool that reported a commit",
			input:       `{"tool_input":{"command":"git commit -m x"},"tool_response":{"gitOperation":{"commit":{"sha":"abc1234","branch":"main"}}}}`,
			wantCommand: "git commit -m x",
			wantCommit:  true,
		},
		{
			name:        "a host that reports no commit beside the command",
			input:       `{"tool_input":{"command":"git commit -m x"},"tool_response":{"stdout":"","interrupted":false}}`,
			wantCommand: "git commit -m x",
		},
		{
			name:  "a tool that answered with something other than an object",
			input: `{"session_id":"s1","tool_input":"raw","tool_response":"raw"}`,
		},
		{
			name:  "a commit member explicitly absent",
			input: `{"tool_response":{"gitOperation":{"commit":null}}}`,
		},
		{name: "no tool members at all", input: `{"session_id":"s1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lifecycle.ReadHookInput(strings.NewReader(tt.input))
			if got.Command() != tt.wantCommand {
				t.Errorf("Command = %q, want %q", got.Command(), tt.wantCommand)
			}
			if got.ReportedCommit() != tt.wantCommit {
				t.Errorf("ReportedCommit = %v, want %v", got.ReportedCommit(), tt.wantCommit)
			}
		})
	}
}

// TestHookInputSurvivesAToolAnswerThatIsNotAnObject pins the reason the
// tool members stay raw: one tool answering with a string must not cost
// the caller the session identity in the same input.
func TestHookInputSurvivesAToolAnswerThatIsNotAnObject(t *testing.T) {
	got := lifecycle.ReadHookInput(strings.NewReader(`{"session_id":"s1","cwd":"/work","tool_response":"plain text"}`))
	if got.SessionID != "s1" || got.Cwd != "/work" {
		t.Errorf("got %+v, want the session identity kept", got)
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
	e.sandbox.RegisterSessionFile(proxytest.ProjectIDHash(root), path, subpath)
}

// registeredFiles returns root's registered entries, cursors included.
func (e *env) registeredFiles(root string) []proxytest.RegisteredFile {
	e.t.Helper()
	return e.sandbox.RegisteredFiles(proxytest.ProjectIDHash(root))
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
	e.projectSettings().Inject(e.deps.ExecPath, "http://127.0.0.1:41100/t/tok-proj")
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
	if !slices.Contains(e.registeredPaths(root), bad) {
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
				ProjectIDHash: proxytest.ProjectIDHash(root),
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
			if seg.Capture.ProjectIDHash != proxytest.ProjectIDHash(root) {
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

	e.sandbox.RewindCursor(proxytest.ProjectIDHash(root), main)

	e.machine().ReadSessionFiles(e.project, discardIO())
	if got := e.storedRecords(); len(got) != 1 {
		t.Fatalf("records after the rerun = %d, want the segment stored once, not twice", len(got))
	}
}

// isolateForSpawn re-homes the device onto files a spawned process
// resolves for itself, so a proxy this test starts writes the same
// stores the machine reads.
func (e *env) isolateForSpawn() *proxytest.SpawnedDevice {
	e.t.Helper()
	device := proxytest.SpawnDevice(e.t, e.deps.Home, e.deps.Version, e.service.URL())
	e.deps.Layout, e.deps.ExecPath, e.deps.ProxyAddr = device.Layout, device.ExecPath, device.ProxyAddr
	e.sandbox = device.Sandbox
	e.seedDeviceToken()
	return device
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
			device := e.isolateForSpawn()
			e.enableProject()
			tt.inject(e)
			root := e.canonicalRoot()
			main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", `{"type":"assistant","message":{"id":"m1"}}`+"\n")
			e.registerFile(root, main, "")

			e.machine().ReadSessionFiles(e.project, discardIO())

			waitHealthy(t, e, e.deps.ProxyAddr)
			if !device.ResidentProcessIsOurs() {
				t.Fatal("the proxy address is held by something other than a healthy proxy of ours")
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

	e.machine().SessionEnded(e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project})

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
