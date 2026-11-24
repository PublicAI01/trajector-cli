package lifecycle_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
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

// oneShotReaderOf is the command line of the one-shot reader of
// project, as a spawned trajector records it.
func oneShotReaderOf(project string) string {
	return strings.Join(claudesettings.HookArgv(claudesettings.HookRead, project), " ")
}

const (
	firstLine  = `{"type":"user","message":{"role":"user","content":"one"},"uuid":"u-1"}` + "\n"
	secondLine = `{"type":"user","message":{"role":"user","content":"two"},"uuid":"u-2"}` + "\n"
)

func TestAHookLeavesARegistryWaitingForItsMigrationToAReaderItStartsAndKeepsTheFileItNamed(t *testing.T) {
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
			before := e.sandbox.RegistryOnDiskButPending(proxytest.ProjectIDHash(root))

			start := time.Now()
			e.machine().SessionProgressed(t.Context(), e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "Stop"})
			took := time.Since(start)

			if took > time.Second {
				t.Errorf("the hook took %v, want it back without waiting on any lock of the migration", took)
			}
			if after := e.sandbox.RegistryOnDiskButPending(proxytest.ProjectIDHash(root)); after != before {
				t.Errorf("registry after the hook =\n%s\nwant it as it was, nothing registered or marked hot:\n%s", after, before)
			}
			if pending := e.sandbox.PendingRegistrations(proxytest.ProjectIDHash(root)); !slices.Equal(pending, []string{main}) {
				t.Errorf("pending registrations = %q, want the session file the hook named, kept for the migration", pending)
			}
			if args, _ := trajector.AwaitRun(10 * time.Second); args != oneShotReaderOf(e.project) {
				t.Errorf("the hook started %q, want the one-shot reader of the project", args)
			}
		})
	}
}

func TestASessionFileAHookNamedWhileAnotherProcessMigratedIsReadOnceTheMigrationIsDone(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	trajector := proxytest.InstalledTrajector(t, e.deps.Home)
	installed := e.deps.ExecPath
	e.deps.ExecPath = trajector.Path()
	e.aProxylessTarget()
	e.enableProject()
	e.injectWithoutBaseURL()
	root := e.canonicalRoot()
	earlier := e.putSessionFile("-work-sample/earlier.jsonl", firstLine)
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", secondLine)
	e.leaveEarlierRegistry(root, earlier, int64(len(firstLine)))
	release := e.sandbox.MigratingElsewhere(proxytest.ProjectIDHash(root))

	e.machine().SessionProgressed(t.Context(), e.project, lifecycle.HookInput{SessionPath: main, Cwd: e.project, HookEvent: "Stop"})
	if args, _ := trajector.AwaitRun(10 * time.Second); args != oneShotReaderOf(e.project) {
		t.Fatalf("the hook started %q, want the one-shot reader of the project", args)
	}
	release()
	e.deps.ExecPath = installed
	e.machine().ReadSessionFiles(t.Context(), e.project, discardIO())

	files := e.registeredFiles(root)
	if len(files) != 2 || files[0].Path != main || files[0].Offset != int64(len(secondLine)) || files[1].Path != earlier {
		t.Errorf("registered = %+v, want the file the hook named read to its end beside the earlier entry", files)
	}
	recs := e.storedRecords()
	if len(recs) != 1 || !strings.Contains(string(recs[0].Raw), "two") {
		t.Errorf("records = %+v, want one segment of the file the hook named", recs)
	}
}

func TestTheReaderAHookStartsMigratesTheRegistryAndReadsItWithTheFileTheHookNamed(t *testing.T) {
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
	for len(e.storedRecords()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the spawned reader stored %+v, want a segment of each file", e.storedRecords())
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitHealthy(t, e, e.deps.ProxyAddr)
	if v := e.registryVersion(root); v != 2 {
		t.Fatalf("registry version = %d, want it migrated to 2", v)
	}
	files := e.registeredFiles(root)
	if len(files) != 2 || files[0].Path != main || files[0].Offset != int64(len(firstLine)) ||
		files[1].Path != earlier || files[1].Offset != int64(len(firstLine+secondLine)) {
		t.Errorf("registered = %+v, want the earlier entry read on from its cursor and the file the hook named read from byte 0", files)
	}
	bySession := map[string]string{}
	for _, rec := range e.storedRecords() {
		bySession[rec.SessionID] += string(rec.Raw)
	}
	if got := bySession["earlier"]; !strings.Contains(got, "two") || strings.Contains(got, "one") {
		t.Errorf("records of the earlier file = %s, want one segment of the line past the earlier cursor", got)
	}
	if got := bySession["0f1e2d3c"]; !strings.Contains(got, "one") {
		t.Errorf("records of the file the hook named = %s, want its line", got)
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

func TestFinishUpgradeLeavesAnEarlierRegistryToAReaderItStarts(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	trajector := proxytest.InstalledTrajector(t, e.deps.Home)
	e.deps.ExecPath = trajector.Path()
	e.enableProject()
	root := e.canonicalRoot()
	main := e.putSessionFile("-work-sample/0f1e2d3c.jsonl", firstLine)
	e.leaveEarlierRegistry(root, main, int64(len(firstLine)))
	e.enabledElsewhere("tok-broken", proxytest.WithProxy)
	broken := filepath.Join(e.layout().FollowDir(), "hash-tok-broken.json")
	if err := os.WriteFile(broken, []byte(`{"version":1,"files":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := e.sandbox.RegistryOnDisk(proxytest.ProjectIDHash(root))

	if err := e.machine().FinishUpgrade(t.Context()); err != nil {
		t.Errorf("FinishUpgrade() = %v, want a project that waits for its migration left to a reader", err)
	}

	if after := e.sandbox.RegistryOnDisk(proxytest.ProjectIDHash(root)); after != before {
		t.Errorf("registry after the finish =\n%s\nwant it as it was: the build that runs the finish stops it after a time of its own, and a migration stopped there holds the project\n%s", after, before)
	}
	if args, _ := trajector.AwaitRun(10 * time.Second); args != oneShotReaderOf(root) {
		t.Errorf("the finish started %q, want the one-shot reader of the project, which migrates it", args)
	}
}
