package lifecycle_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

const (
	// workspaceTrustLine is the sentence doctor no longer says: an
	// unregistered session file is not a reading of the trust dialog,
	// and no surface may present it as one.
	workspaceTrustLine = "This workspace is not trusted yet; accept the trust dialog in Claude Code."
	sessionMarker      = "sid-MARKER"
)

// registryBytes is the project's registry file as it stands, so a
// surface can be shown to have left it alone.
func (e *env) registryBytes() string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.layout().FollowDir(), proxytest.ProjectIDHash(e.canonicalRoot())+".json"))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data)
}

// lockedSubdir makes a directory of the project that cannot be listed,
// so a walk over the tree has something to report.
func (e *env) lockedSubdir(name string) string {
	e.t.Helper()
	if os.Geteuid() == 0 {
		e.t.Skip("root lists every directory")
	}
	dir := filepath.Join(e.canonicalRoot(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { os.Chmod(dir, 0o700) })
	return dir
}

// enabledWithOneSessionAndOneUnreported enables the project with one
// session file already there, then leaves one more that no hook
// reported and a directory the walk cannot list. The second file is
// written after the project was enabled, so nothing but a hook could
// have reported it. A surface that asks for a reading needs a proxy
// that takes the report, so the caller hands one in.
func (e *env) enabledWithOneSessionAndOneUnreported(opts ...proxytest.Option) (locked string) {
	e.t.Helper()
	e.startProxy(opts...)
	e.sessionFile(sessionMarker+"-1", time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC))
	e.enable(proxytest.WithProxy)
	e.sessionFile(sessionMarker+"-2", e.deps.Now().Add(time.Hour))
	locked = e.lockedSubdir("vendor")
	e.stdout.Reset()
	return locked
}

func TestStatusReadsTheRegistryWithoutWalkingOrRegistering(t *testing.T) {
	e := newEnv(t)
	locked := e.enabledWithOneSessionAndOneUnreported()
	before := e.registryBytes()

	out := e.statusOutput()

	for _, want := range []string{
		"Contributing",
		"Session files: 1 session(s) registered; last read never;",
		"Sessions started under another spelling of this project's path",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
	// The walk is doctor's: status would only know about the directory
	// it cannot list, and the session no hook reported, by walking.
	for _, unwanted := range []string{sessionMarker, e.sessionFilesRoot(), locked, "Not looked at"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("status = %q, want no %q", out, unwanted)
		}
	}
	if got := e.registryBytes(); got != before {
		t.Errorf("status changed the registry:\n%s\nwas:\n%s", got, before)
	}
}

func TestDoctorWalksTheProjectWithoutRegistering(t *testing.T) {
	e := newEnv(t)
	var res resident
	locked := e.enabledWithOneSessionAndOneUnreported(res.handler())
	before := e.registeredFiles(e.canonicalRoot())

	problems, out := e.doctor()

	for _, want := range []string{
		"warning: could not determine why the hooks did not report 1 session(s) of this project",
		"note: Not looked at: " + locked + " could not be listed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor = %q, want it to contain %q", out, want)
		}
	}
	if problems != 0 {
		t.Errorf("problems = %d, want a state this device cannot explain counted as none", problems)
	}
	for _, unwanted := range []string{sessionMarker, "Remote Control", workspaceTrustLine} {
		if strings.Contains(out, unwanted) {
			t.Errorf("doctor = %q, want no %q", out, unwanted)
		}
	}
	after := e.registeredFiles(e.canonicalRoot())
	if len(after) != len(before) {
		t.Fatalf("registered = %+v, want the one file enable registered and nothing doctor found", after)
	}
	for i, f := range after {
		was := before[i]
		if f.Path != was.Path || f.Retired != was.Retired {
			t.Errorf("registry entry = %+v, was %+v", f, was)
		}
		if f.NextSegment != was.NextSegment || f.Offset != was.Offset || !slices.Equal(f.MessageIDs, was.MessageIDs) {
			t.Errorf("doctor moved a cursor: %+v, was %+v", f, was)
		}
	}
}

func TestDoctorRegistersTheSessionsThatPredateTheGrantAndHasThemRead(t *testing.T) {
	e := newEnv(t)
	var res resident
	e.startProxy(res.handler())
	e.enable(proxytest.WithProxy)
	// A file this project already had when it was enabled, which
	// nothing registered: the shape an install made by a build that
	// registered none of them leaves behind.
	earlier := e.sessionFile(sessionMarker+"-1", e.deps.Now().Add(-48*time.Hour))
	e.stdout.Reset()

	problems, out := e.doctor()

	if problems != 0 {
		t.Errorf("problems = %d, want none", problems)
	}
	if !strings.Contains(out, "fixed: registered 1 session file(s) of this project that no hook reported, and asked for them to be read") {
		t.Errorf("doctor = %q, want the earlier session registered and read", out)
	}
	if paths := e.registeredPaths(e.canonicalRoot()); len(paths) != 1 || paths[0] != earlier {
		t.Errorf("registered = %v, want %q", paths, earlier)
	}
	want := []apiproxy.Progress{{ProjectIDHash: e.status().Hash, Path: earlier, End: true}}
	if got := res.reports(); !slices.Equal(got, want) {
		t.Errorf("the resident process was told %+v, want %+v", got, want)
	}
	if strings.Contains(out, "predate its grant") {
		t.Errorf("doctor = %q, want no leftover note about what it has just registered", out)
	}
}
func TestDoctorRegistersOnlyTheSessionsThatPredateTheGrantAndKeepsReportingTheRest(t *testing.T) {
	e := newEnv(t)
	var res resident
	e.startProxy(res.handler())
	e.enable(proxytest.WithProxy)
	earlier := e.sessionFile(sessionMarker+"-1", e.deps.Now().Add(-48*time.Hour))
	e.sessionFile(sessionMarker+"-2", e.deps.Now().Add(time.Hour))
	e.stdout.Reset()

	_, out := e.doctor()

	if paths := e.registeredPaths(e.canonicalRoot()); !slices.Equal(paths, []string{earlier}) {
		t.Errorf("registered = %v, want only the session that predates the grant (%q)", paths, earlier)
	}
	want := []apiproxy.Progress{{ProjectIDHash: e.status().Hash, Path: earlier, End: true}}
	if got := res.reports(); !slices.Equal(got, want) {
		t.Errorf("the resident process was told %+v, want %+v", got, want)
	}
	if !strings.Contains(out, "warning: could not determine why the hooks did not report 1 session(s) of this project") {
		t.Errorf("doctor = %q, want the session written after the grant still reported", out)
	}

	e.stdout.Reset()
	_, again := e.doctor()

	if !strings.Contains(again, "warning: could not determine why the hooks did not report 1 session(s) of this project") {
		t.Errorf("second doctor = %q, want the unexplained session reported again", again)
	}
}

// registeredButNeverRead leaves what a build that registered a
// project's session files without ever reading them left behind: an
// entry with a cursor at nothing, whose file is on disk with lines in
// it. The file is written after the project was enabled, so nothing
// about it is a session the walk finds unregistered.
func (e *env) registeredButNeverRead() string {
	e.t.Helper()
	path := e.sessionFile(sessionMarker+"-1", e.deps.Now().Add(time.Hour))
	e.registerFile(e.canonicalRoot(), path, "")
	return path
}

func TestDoctorAsksForARegisteredSessionFileThatWasNeverRead(t *testing.T) {
	e := newEnv(t)
	var res resident
	e.startProxy(res.handler())
	e.enable(proxytest.WithProxy)
	path := e.registeredButNeverRead()
	e.stdout.Reset()

	problems, out := e.doctor()

	if problems != 0 {
		t.Errorf("problems = %d, want none", problems)
	}
	if !strings.Contains(out, "fixed: asked for 1 never-read session file(s) of this project to be read") {
		t.Errorf("doctor = %q, want the never-read session asked for", out)
	}
	want := []apiproxy.Progress{{ProjectIDHash: e.status().Hash, Path: path, End: true}}
	if got := res.reports(); !slices.Equal(got, want) {
		t.Errorf("the resident process was told %+v, want %+v", got, want)
	}
	files := e.registeredFiles(e.canonicalRoot())
	if len(files) != 1 || files[0].LastEvent == "" {
		t.Errorf("registry = %+v, want the entry left where the sweep looks", files)
	}
}

func TestDoctorSaysTheNeverReadSessionFilesCouldNotBeAskedFor(t *testing.T) {
	e := newEnv(t)
	e.deps.Spawn = noReaderStarts()
	e.enable(proxytest.WithoutProxy)
	e.registeredButNeverRead()
	e.stdout.Reset()

	problems, out := e.doctor()

	if problems != 1 {
		t.Errorf("problems = %d, want a reading no reader took counted as one", problems)
	}
	for _, want := range []string{"could not be asked for", "The next session"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor = %q, want it to contain %q", out, want)
		}
	}
	for _, unwanted := range []string{"fixed: asked for", "Everything checks out."} {
		if strings.Contains(out, unwanted) {
			t.Errorf("doctor = %q, want no %q", out, unwanted)
		}
	}
}

func TestDoctorSaysTheSessionFilesCouldNotBePutBackOnTheReadingPath(t *testing.T) {
	e := newEnv(t)
	var res resident
	e.startProxy(res.handler())
	e.enable(proxytest.WithProxy)
	e.registeredButNeverRead()
	e.readOnlyDir(e.layout().FollowDir())
	e.stdout.Reset()

	problems, out := e.doctor()

	if problems == 0 {
		t.Errorf("doctor = %q, want the repair it could not make counted as a problem", out)
	}
	if !strings.Contains(out, "could not be put back on the path that reads them") {
		t.Errorf("doctor = %q, want it to say the repair failed", out)
	}
	if strings.Contains(out, "Everything checks out.") {
		t.Errorf("doctor = %q, want no all-clear after a failed repair", out)
	}
}

func TestDoctorDoesNotCountASessionFileAnEarlierBuildRetiredAsUnregistered(t *testing.T) {
	e := newEnv(t)
	var res resident
	e.startProxy(res.handler())
	e.enable(proxytest.WithProxy)
	path := e.sessionFile(sessionMarker+"-1", e.deps.Now().Add(-48*time.Hour))
	appendLine(t, path, `{"type":"relocated","relocatedCwd":"/elsewhere/entirely"}`)
	e.registerFile(e.canonicalRoot(), path, "")
	e.sandbox.RetireAsAnEarlierBuild(e.status().Hash, path)
	e.stdout.Reset()

	_, out := e.doctor()
	e.stdout.Reset()
	status := e.statusOutput()

	if !strings.Contains(out, "every session file of this project is registered") {
		t.Errorf("doctor = %q, want an entry an earlier build retired counted as registered", out)
	}
	for _, unwanted := range []string{"predate its grant", "to register them", "could not determine why"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("doctor = %q, want no %q about a file an earlier build retired", out, unwanted)
		}
		if strings.Contains(status, unwanted) {
			t.Errorf("status = %q, want no %q about a file an earlier build retired", status, unwanted)
		}
	}
}

func TestDoctorLeavesNeverReadSessionsAloneWhereEarlierSessionsWereSkipped(t *testing.T) {
	e := newEnv(t)
	var res resident
	e.startProxy(res.handler())
	if err := e.machine().Enable(e.project, lifecycle.EnableChoices{Shape: proxytest.WithProxy, SkipEarlier: true}, e.io()); err != nil {
		t.Fatalf("enable: %v\nstdout: %s\nstderr: %s", err, e.stdout, e.stderr)
	}
	path := e.sessionFile(sessionMarker+"-1", e.deps.Now().Add(-48*time.Hour))
	e.registerFile(e.canonicalRoot(), path, "")
	before := e.registryBytes()
	e.stdout.Reset()

	_, out := e.doctor()

	if got := e.registryBytes(); got != before {
		t.Errorf("doctor changed the registry:\n%s\nwas:\n%s", got, before)
	}
	if got := res.reports(); len(got) != 0 {
		t.Errorf("the resident process was told %+v about a project enabled without its earlier sessions", got)
	}
	if strings.Contains(out, "never-read") {
		t.Errorf("doctor = %q, want no reading asked where the user asked for none", out)
	}
}
func TestDoctorAsksForANeverReadSessionWrittenAfterAGrantThatSkippedTheEarlierOnes(t *testing.T) {
	e := newEnv(t)
	var res resident
	e.startProxy(res.handler())
	if err := e.machine().Enable(e.project, lifecycle.EnableChoices{Shape: proxytest.WithProxy, SkipEarlier: true}, e.io()); err != nil {
		t.Fatalf("enable: %v\nstdout: %s\nstderr: %s", err, e.stdout, e.stderr)
	}
	earlier := e.sessionFile(sessionMarker+"-1", e.deps.Now().Add(-48*time.Hour))
	e.registerFile(e.canonicalRoot(), earlier, "")
	later := e.sessionFile(sessionMarker+"-2", e.deps.Now().Add(time.Hour))
	e.registerFile(e.canonicalRoot(), later, "")
	e.stdout.Reset()

	_, out := e.doctor()

	if !strings.Contains(out, "fixed: asked for 1 never-read session file(s) of this project to be read") {
		t.Errorf("doctor = %q, want the session written after the grant asked for", out)
	}
	want := []apiproxy.Progress{{ProjectIDHash: e.status().Hash, Path: later, End: true}}
	if got := res.reports(); !slices.Equal(got, want) {
		t.Errorf("the resident process was told %+v, want %+v", got, want)
	}
	for _, f := range e.registeredFiles(e.canonicalRoot()) {
		if f.Path == earlier && f.LastEvent != "" {
			t.Errorf("registry = %+v, want the entry that predates the grant left as it was", f)
		}
	}
}

func TestStatusShowsWhenAFileWasLastReadAndWhatIsNotReadYet(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	path := e.sessionFile("s-1", time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC))
	e.enable(proxytest.WithProxy)
	e.machine().ReadSessionFiles(e.project, e.io())
	e.stdout.Reset()

	out := e.statusOutput()
	want := "Session files: 1 session(s) registered; last read 2026-08-02T12:00:00Z; 0 B not read yet."
	if !strings.Contains(out, want) {
		t.Errorf("status = %q, want it to contain %q", out, want)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Repeat("x", 100)); err != nil {
		t.Fatal(err)
	}
	f.Close()
	e.stdout.Reset()

	out = e.statusOutput()
	want = "Session files: 1 session(s) registered; last read 2026-08-02T12:00:00Z; 100 B not read yet."
	if !strings.Contains(out, want) {
		t.Errorf("status = %q, want it to contain %q", out, want)
	}
}

func TestStatusReportsAMissingSessionHookThatDoctorAdds(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.enable(proxytest.WithProxy)
	e.dropSessionEndHook()
	e.stdout.Reset()

	out := e.statusOutput()
	for _, want := range []string{"Contributing", "a session hook is missing", e.settingsPath(), "fix:  trajector doctor"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "disagree") {
		t.Errorf("status = %q, want a project short of one hook presented as contributing, not as a disagreement", out)
	}

	e.stdout.Reset()
	if _, out := e.doctor(); !strings.Contains(out, "session hooks restored") {
		t.Errorf("doctor = %q, want the hook restored", out)
	}
	e.stdout.Reset()
	if out := e.statusOutput(); strings.Contains(out, "session hook is missing") {
		t.Errorf("status = %q, want nothing missing after doctor", out)
	}
}

// TestStatusReportsAMissingGitSnapshotHookThatDoctorAdds covers the
// injection an upgrade inherits: everything an earlier release wrote
// stands, and the hook this release adds does not.
func TestStatusReportsAMissingGitSnapshotHookThatDoctorAdds(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.enable(proxytest.WithProxy)
	e.dropHook(proxytest.GitSnapshotMarker)
	e.stdout.Reset()

	if out := e.statusOutput(); !strings.Contains(out, "a session hook is missing") || !strings.Contains(out, e.settingsPath()) {
		t.Errorf("status = %q, want the missing hook named", out)
	}
	e.stdout.Reset()
	if _, out := e.doctor(); !strings.Contains(out, "session hooks restored") {
		t.Errorf("doctor = %q, want the hook restored", out)
	}
	if !e.projectSettings().HasHook(proxytest.GitSnapshotMarker) {
		t.Error("doctor reported the injection restored without the hook it lacked")
	}
}

func TestStatusAndDoctorTakeAnIdleProxyAsNormalWhereNoProjectUsesIt(t *testing.T) {
	const idle = "on this device it runs only while a session is open"
	t.Run("every enabled project records without the proxy", func(t *testing.T) {
		e := newEnv(t)
		root := e.canonicalRoot()
		e.sandbox.GrantProject(proxytest.Grant{
			Token:         "tok-proj",
			ProjectIDHash: proxytest.ProjectIDHash(root),
			RootPath:      root,
			Upstream:      "https://api.anthropic.com",
			Shape:         proxytest.WithoutProxy,
		})
		e.injectWithoutBaseURL()
		e.aProxylessTarget()

		out := e.statusOutput()
		if !strings.Contains(out, "Not running; "+idle) || strings.Contains(out, "starts on demand") {
			t.Errorf("status = %q, want the idle proxy stated as normal", out)
		}
		e.stdout.Reset()
		problems, out := e.doctor()
		if !strings.Contains(out, "ok: proxy not running; "+idle) {
			t.Errorf("doctor = %q, want the idle proxy passed", out)
		}
		for _, unwanted := range []string{"started the capture proxy", "could not be started", "problem:"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("doctor = %q, want no %q", out, unwanted)
			}
		}
		if problems != 0 {
			t.Errorf("problems = %d, want none on a healthy device", problems)
		}
	})
	t.Run("a project that routes through the proxy still needs it", func(t *testing.T) {
		e := newEnv(t)
		e.enableProject()
		e.injectWithProxy()
		e.aProxylessTarget()

		out := e.statusOutput()
		if strings.Contains(out, idle) {
			t.Errorf("status = %q, want no idle-proxy sentence while a project routes through it", out)
		}
		e.stdout.Reset()
		if problems, out := e.doctor(); problems == 0 || !strings.Contains(out, "could not be started") {
			t.Errorf("problems = %d, doctor = %q; want the absent proxy reported", problems, out)
		}
	})
}

func TestStatusTellsARedactionPauseFromAnAgreementPause(t *testing.T) {
	e := newEnv(t)
	e.sandbox.Pause(proxytest.PauseRedactionDrift)
	out := e.statusOutput()
	if !strings.Contains(out, "Recording is paused everywhere") || !strings.Contains(out, "read without the newline that ends it") {
		t.Errorf("status = %q, want the redaction pause explained", out)
	}
	if strings.Contains(out, "agreement") {
		t.Errorf("status = %q, want no word about the agreement", out)
	}

	e.sandbox.Pause(proxytest.PauseConsentReconfirm)
	e.stdout.Reset()
	out = e.statusOutput()
	if !strings.Contains(out, "the data agreement changed") || strings.Contains(out, "redaction") {
		t.Errorf("status = %q, want the agreement pause and nothing about redaction", out)
	}
}

func TestStatusNeverListsASessionId(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.sessionFile(sessionMarker+"-1", time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC))
	e.enable(proxytest.WithProxy)
	e.stdout.Reset()

	out := e.statusOutput()
	if !strings.Contains(out, "Session files: 1 session(s) registered") {
		t.Errorf("status = %q, want the session counted", out)
	}
	for _, unwanted := range []string{sessionMarker, e.sessionFilesRoot()} {
		if strings.Contains(out, unwanted) {
			t.Errorf("status = %q, want no %q", out, unwanted)
		}
	}
}

func TestDoctorBundleCarriesSessionCountsWithoutIdsOrPaths(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.sessionFile(sessionMarker+"-1", time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC))
	e.enable(proxytest.WithProxy)

	t.Chdir(t.TempDir())
	path, err := e.machine().DoctorBundle(e.project, e.io())
	if err != nil {
		t.Fatal(err)
	}
	entries := readBundle(t, path)
	diagnosis := entries["diagnosis.json"]
	for _, want := range []string{`"sessions": 1`, `"no_proxy": false`, `"session_end_installed": true`, `"hook_policy"`, `"bytes_behind"`} {
		if !strings.Contains(diagnosis, want) {
			t.Errorf("diagnosis.json = %s, want it to contain %q", diagnosis, want)
		}
	}
	for name, data := range entries {
		for _, unwanted := range []string{sessionMarker, e.sessionFilesRoot(), ".jsonl"} {
			if strings.Contains(data, unwanted) {
				t.Errorf("bundle entry %s contains %q", name, unwanted)
			}
		}
	}
}

func TestDoctorBundleCarriesTheSessionsNoHookReported(t *testing.T) {
	e := newEnv(t)
	locked := e.enabledWithOneSessionAndOneUnreported()

	t.Chdir(t.TempDir())
	path, err := e.machine().DoctorBundle(e.project, e.io())
	if err != nil {
		t.Fatal(err)
	}
	entries := readBundle(t, path)
	diagnosis := entries["diagnosis.json"]
	for _, want := range []string{`"walked": true`, `"unregistered": 1`, `"unreadable": 1`} {
		if !strings.Contains(diagnosis, want) {
			t.Errorf("diagnosis.json = %s, want it to contain %q", diagnosis, want)
		}
	}
	for name, data := range entries {
		for _, unwanted := range []string{sessionMarker, locked, ".jsonl"} {
			if strings.Contains(data, unwanted) {
				t.Errorf("bundle entry %s contains %q", name, unwanted)
			}
		}
	}
}

func TestStatusAndDoctorReportARegistryTheyCannotRead(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := newEnv(t)
	e.startProxy()
	e.enable(proxytest.WithProxy)
	e.obstruct(e.layout().FollowDir())
	e.stdout.Reset()

	out := e.statusOutput()
	if !strings.Contains(out, "warning: the session file registry could not be read") {
		t.Errorf("status = %q, want the unreadable registry reported, not an empty one", out)
	}
	if strings.Contains(out, "Session files: none registered yet") {
		t.Errorf("status = %q, want no claim about what the registry holds", out)
	}
	e.stdout.Reset()
	problems, out := e.doctor()
	if problems == 0 || !strings.Contains(out, "error: the session file registry could not be read") {
		t.Errorf("problems = %d, doctor = %q; want the unreadable registry counted", problems, out)
	}
}

func TestStatusReportsTheDiscoveryHookClaudeCodeNoLongerReadsAndRemovesNothing(t *testing.T) {
	e := newEnv(t)
	e.movedConfigDir()
	settings := e.hookInDefaultConfigDir()

	out := e.statusOutput()
	for _, want := range []string{
		"a trajector hook is left in ~/.claude/settings.json and never runs",
		"fix:  trajector doctor",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}
	if !settings.HasHook(proxytest.DiscoveryMarker) {
		t.Error("status removed the hook instead of reporting it")
	}
}
