package lifecycle_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

const (
	workspaceTrustLine = "This workspace is not trusted yet; accept the trust dialog in Claude Code."
	sessionMarker      = "sid-MARKER"
)

// registryBytes is the project's registry file as it stands, so a
// surface can be shown to have left it alone.
func (e *env) registryBytes() string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.layout().FollowDir(), consent.ProjectIDHash(e.canonicalRoot())+".json"))
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
// reported and a directory the walk cannot list.
func (e *env) enabledWithOneSessionAndOneUnreported() (locked string) {
	e.t.Helper()
	e.startProxy()
	e.sessionFile(sessionMarker+"-1", time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC))
	e.enable(false)
	e.sessionFile(sessionMarker+"-2", time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC))
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
	locked := e.enabledWithOneSessionAndOneUnreported()
	before := e.registryBytes()

	problems, out := e.doctor()

	for _, want := range []string{
		"problem: " + workspaceTrustLine,
		"1 session(s) of this project were written without a hook of trajector's reporting them",
		"note: Not looked at: " + locked + " could not be listed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor = %q, want it to contain %q", out, want)
		}
	}
	if problems == 0 {
		t.Error("problems = 0, want the untrusted workspace counted")
	}
	for _, unwanted := range []string{sessionMarker, "Remote Control"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("doctor = %q, want no %q", out, unwanted)
		}
	}
	if got := e.registryBytes(); got != before {
		t.Errorf("doctor changed the registry:\n%s\nwas:\n%s", got, before)
	}
	if paths := e.registeredPaths(e.canonicalRoot()); len(paths) != 1 {
		t.Errorf("registered = %v, want the one file enable registered and nothing doctor found", paths)
	}
}

func TestStatusShowsWhenAFileWasLastReadAndWhatIsNotReadYet(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	path := e.sessionFile("s-1", time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC))
	e.enable(false)
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

func TestStatusReportsAMissingSessionEndHookThatDoctorAdds(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.enable(false)
	e.dropSessionEndHook()
	e.stdout.Reset()

	out := e.statusOutput()
	for _, want := range []string{"Contributing", "The session-end hook is missing from " + e.settingsPath() + "; run `trajector doctor` to add it."} {
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
	if out := e.statusOutput(); strings.Contains(out, "session-end hook is missing") {
		t.Errorf("status = %q, want nothing missing after doctor", out)
	}
}

func TestStatusAndDoctorTakeAnIdleProxyAsNormalWhereNoProjectUsesIt(t *testing.T) {
	const idle = "on this device it runs only while a session is open"
	t.Run("every enabled project records without the proxy", func(t *testing.T) {
		e := newEnv(t)
		root := e.canonicalRoot()
		e.sandbox.GrantProject(proxytest.Grant{
			Token:         "tok-proj",
			ProjectIDHash: consent.ProjectIDHash(root),
			RootPath:      root,
			Upstream:      "https://api.anthropic.com",
			NoProxy:       true,
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
	e.sandbox.Pause(routing.PauseRedactionDrift)
	out := e.statusOutput()
	if !strings.Contains(out, "Recording is paused everywhere") || !strings.Contains(out, "redaction does not cover") {
		t.Errorf("status = %q, want the redaction pause explained", out)
	}
	if strings.Contains(out, "agreement") {
		t.Errorf("status = %q, want no word about the agreement", out)
	}

	e.sandbox.Pause(routing.PauseConsentReconfirm)
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
	e.enable(false)
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
	e.enable(false)

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

func TestStatusAndDoctorReportARegistryTheyCannotRead(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.enable(false)
	e.obstruct(e.layout().FollowDir())
	e.stdout.Reset()

	out := e.statusOutput()
	if !strings.Contains(out, "WARNING: the session file registry could not be read") {
		t.Errorf("status = %q, want the unreadable registry reported, not an empty one", out)
	}
	if strings.Contains(out, "Session files: none registered yet") {
		t.Errorf("status = %q, want no claim about what the registry holds", out)
	}
	e.stdout.Reset()
	problems, out := e.doctor()
	if problems == 0 || !strings.Contains(out, "problem: the session file registry could not be read") {
		t.Errorf("problems = %d, doctor = %q; want the unreadable registry counted", problems, out)
	}
}
