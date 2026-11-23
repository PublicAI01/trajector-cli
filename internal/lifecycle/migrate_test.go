package lifecycle_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

// leaveEarlierRegistry puts root's registry on disk as a build before
// the per-entry layout left it, holding one entry for path read up to
// offset.
func (e *env) leaveEarlierRegistry(root, path string, offset int64) {
	e.t.Helper()
	e.sandbox.LeaveEarlierRegistry(proxytest.ProjectIDHash(root), proxytest.EarlierRegistry{
		Files: []proxytest.EarlierEntry{{Path: path, Size: offset, Offset: offset, NextSegment: 1, MessageIDs: []string{"m1"}}},
	})
}

func (e *env) registryVersion(root string) int {
	e.t.Helper()
	return e.sandbox.RegistryVersion(proxytest.ProjectIDHash(root))
}

// wantMigratedWithItsCursor fails unless root's registry is in this
// build's layout and its entry for path stands where the earlier build
// left it.
func (e *env) wantMigratedWithItsCursor(root, path string, offset int64) {
	e.t.Helper()
	if v := e.registryVersion(root); v != 2 {
		e.t.Fatalf("registry version = %d, want it migrated to 2", v)
	}
	files := e.registeredFiles(root)
	if len(files) != 1 || files[0].Path != path || files[0].Offset < offset {
		e.t.Errorf("registered = %+v, want the earlier entry of %s with its cursor at %d or past it", files, path, offset)
	}
}

const (
	firstLine  = `{"type":"user","message":{"role":"user","content":"one"},"uuid":"u-1"}` + "\n"
	secondLine = `{"type":"user","message":{"role":"user","content":"two"},"uuid":"u-2"}` + "\n"
)

func TestAHookLeavesARegistryWaitingForItsMigrationAndStartsAReader(t *testing.T) {
	proxytest.RequireSessionSources(t)
	for _, tc := range []struct {
		name  string
		leave func(e *env, root, path string)
	}{
		{"version 1", func(e *env, root, path string) { e.leaveEarlierRegistry(root, path, 0) }},
		{"halfway migrated", func(e *env, root, path string) {
			e.sandbox.LeaveRegistryHalfwayMigrated(proxytest.ProjectIDHash(root), proxytest.EarlierRegistry{
				Files: []proxytest.EarlierEntry{{Path: path}},
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			trajector := proxytest.InstalledTrajector(t, e.deps.Home)
			e.deps.ExecPath = trajector.Path()
			e.enableProject()
			root := e.canonicalRoot()
			earlier := e.putSessionFile("-work-sample/earlier.jsonl", firstLine)
			main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", firstLine)
			tc.leave(e, root, earlier)
			e.sandbox.MigratingElsewhere(proxytest.ProjectIDHash(root))
			before := e.sandbox.RegistryOnDisk(proxytest.ProjectIDHash(root))

			start := time.Now()
			e.machine().SessionProgressed(t.Context(), e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "Stop"})
			took := time.Since(start)

			if took > time.Second {
				t.Errorf("the hook took %v, want it back without waiting on any lock of the migration", took)
			}
			if after := e.sandbox.RegistryOnDisk(proxytest.ProjectIDHash(root)); after != before {
				t.Errorf("registry after the hook =\n%s\nwant it as it was, nothing registered or marked hot:\n%s", after, before)
			}
			if args, _ := trajector.AwaitRun(10 * time.Second); args != "hook read "+e.project {
				t.Errorf("the hook started %q, want the one-shot reader of the project", args)
			}
		})
	}
}

func TestTheReaderAHookStartsMigratesTheRegistryAndReadsIt(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.isolateForSpawn()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	earlier := e.putSessionFile("-work-sample/earlier.jsonl", firstLine+secondLine)
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", firstLine)
	e.leaveEarlierRegistry(root, earlier, int64(len(firstLine)))

	e.machine().SessionEnded(t.Context(), e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project})

	deadline := time.Now().Add(15 * time.Second)
	for len(e.storedRecords()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the spawned reader never stored a segment")
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitHealthy(t, e, e.deps.ProxyAddr)
	e.wantMigratedWithItsCursor(root, earlier, int64(len(firstLine+secondLine)))
	recs := e.storedRecords()
	if len(recs) != 1 || !strings.Contains(string(recs[0].Raw), "two") || strings.Contains(string(recs[0].Raw), "one") {
		t.Errorf("records = %+v, want one segment of the line past the earlier cursor", recs)
	}
}

func TestReadSessionFilesMigratesTheRegistryAndReadsOnFromTheEarlierCursor(t *testing.T) {
	e := newEnv(t)
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", firstLine+secondLine)
	e.leaveEarlierRegistry(root, main, int64(len(firstLine)))

	e.machine().ReadSessionFiles(t.Context(), e.project, discardIO())

	e.wantMigratedWithItsCursor(root, main, int64(len(firstLine+secondLine)))
	recs := e.storedRecords()
	if len(recs) != 1 || !strings.Contains(string(recs[0].Raw), "two") || strings.Contains(string(recs[0].Raw), "one") {
		t.Errorf("records = %+v, want one segment of the line past the earlier cursor", recs)
	}
}

func TestStatusMigratesARegistryAnEarlierBuildLeft(t *testing.T) {
	e := newEnv(t)
	e.enableProject()
	e.injectWithProxy()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", firstLine)
	e.leaveEarlierRegistry(root, main, int64(len(firstLine)))

	out := e.statusOutput()

	if !strings.Contains(out, "Session files: 1 session(s) registered") {
		t.Errorf("status:\n%s\nwant the migrated session counted", out)
	}
	e.wantMigratedWithItsCursor(root, main, int64(len(firstLine)))
}

func TestStatusSaysTheRegistryCannotBeReadWhileAnotherProcessMigratesIt(t *testing.T) {
	e := newEnv(t)
	e.enableProject()
	e.injectWithProxy()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", firstLine)
	e.leaveEarlierRegistry(root, main, 0)
	e.sandbox.MigratingElsewhere(proxytest.ProjectIDHash(root))

	out := e.statusOutput()

	if !strings.Contains(out, "the session file registry could not be read") || !strings.Contains(out, "another process is migrating it") {
		t.Errorf("status:\n%s\nwant the registry stated unreadable for now, with the reason", out)
	}
	if v := e.registryVersion(root); v != 1 {
		t.Errorf("registry version = %d, want it left to the other process at 1", v)
	}
}

func TestStatusSaysAnEarlierRegistryThatDoesNotParseCannotBeRead(t *testing.T) {
	e := newEnv(t)
	e.enableProject()
	e.injectWithProxy()
	file := filepath.Join(e.layout().FollowDir(), proxytest.ProjectIDHash(e.canonicalRoot())+".json")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	const broken = `{"version":1,"files":[{"path":`
	if err := os.WriteFile(file, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	out := e.statusOutput()

	if !strings.Contains(out, "the session file registry could not be read: follow: registry:") {
		t.Errorf("status:\n%s\nwant the registry stated unreadable with the parse error", out)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != broken {
		t.Errorf("project file = %q, %v, want it left as it was", got, err)
	}
}

func TestDoctorMigratesARegistryAnEarlierBuildLeft(t *testing.T) {
	e := newEnv(t)
	e.enableProject()
	e.injectWithProxy()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", firstLine)
	e.leaveEarlierRegistry(root, main, int64(len(firstLine)))

	_, out := e.doctor()

	if strings.Contains(out, "registry could not be read") {
		t.Errorf("doctor:\n%s\nwant the registry read once migrated", out)
	}
	e.wantMigratedWithItsCursor(root, main, int64(len(firstLine)))
}

func TestEnableMigratesARegistryAnEarlierBuildLeftBeforeItRegisters(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	at := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	main := e.sessionFile("s-1", at)
	root := e.canonicalRoot()
	info, err := os.Stat(main)
	if err != nil {
		t.Fatal(err)
	}
	e.leaveEarlierRegistry(root, main, info.Size())

	e.enable(proxytest.WithProxy)

	e.wantMigratedWithItsCursor(root, main, info.Size())
}

func TestFinishUpgradeMigratesTheRegistryOfEveryEnabledProject(t *testing.T) {
	e := newEnv(t)
	e.enableProject()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", firstLine)
	e.leaveEarlierRegistry(root, main, int64(len(firstLine)))
	e.enabledElsewhere("tok-other", proxytest.WithProxy)
	other := e.putSessionFile("-work-other/1a2b3c4d.jsonl", firstLine)
	e.sandbox.LeaveEarlierRegistry("hash-tok-other", proxytest.EarlierRegistry{Files: []proxytest.EarlierEntry{{Path: other}}})
	e.enabledElsewhere("tok-broken", proxytest.WithProxy)
	broken := filepath.Join(e.layout().FollowDir(), "hash-tok-broken.json")
	if err := os.WriteFile(broken, []byte(`{"version":1,"files":[`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.machine().FinishUpgrade(t.Context()); err != nil {
		t.Errorf("FinishUpgrade() = %v, want a project it could not migrate left to the next reader", err)
	}

	e.wantMigratedWithItsCursor(root, main, int64(len(firstLine)))
	if v := e.sandbox.RegistryVersion("hash-tok-other"); v != 2 {
		t.Errorf("registry version of the other project = %d, want 2", v)
	}
}
