package lifecycle_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

func TestInjectedHookQuotesBinaryPathsWithSpaces(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.deps.ExecPath = "/Users/dev/My Tools/trajector"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	settings, err := os.ReadFile(e.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), `\"/Users/dev/My Tools/trajector\" hook ensure-proxy`) {
		t.Errorf("injected hook does not quote the spaced path:\n%s", settings)
	}
}

func TestEnableUsesWallClockWhenNowUnset(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.deps.Now = time.Now
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	route, ok := e.sandbox.ActiveGrant(e.canonicalRoot())
	if !ok || route.GrantedAt == "" {
		t.Errorf("route = %+v, ok = %v", route, ok)
	}
}

func TestEnableFailsWhenAgreementAnswerUnavailable(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.stdin = ""
	err := e.machine().Enable(e.project, e.io())
	if err == nil || !strings.Contains(err.Error(), "agreement answer") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnableRollsBackWhenSettingsFileIsMalformed(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := os.MkdirAll(filepath.Dir(e.settingsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	malformed := `{"env": "not-an-object"}`
	if err := os.WriteFile(e.settingsPath(), []byte(malformed), 0o644); err != nil {
		t.Fatal(err)
	}

	err := e.machine().Enable(e.project, e.io())
	if err == nil {
		t.Fatal("enable succeeded over a malformed settings file")
	}
	data, readErr := os.ReadFile(e.settingsPath())
	if readErr != nil || string(data) != malformed {
		t.Errorf("settings after rollback = %q, %v", data, readErr)
	}
	if e.status().Enabled {
		t.Error("routing grant survived rollback")
	}
}

func TestEnableFailsWhileCapturePaused(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.sandbox.Pause(routing.PauseSignedOut)

	err := e.machine().Enable(e.project, e.io())
	if err == nil || !strings.Contains(err.Error(), "trajector login") {
		t.Fatalf("err = %v, want the pause reason and what to do about it", err)
	}
	if _, err := os.Stat(e.settingsPath()); !os.IsNotExist(err) {
		t.Error("injection survived rollback")
	}
}

func TestEnableFailsWhenSpoolUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not block writes on windows")
	}
	e := newEnv(t)
	spoolDir := e.layout().SpoolDir()
	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	e.startProxy()
	if err := os.Chmod(spoolDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(spoolDir, 0o700) })

	err := e.machine().Enable(e.project, e.io())
	if err == nil || !strings.Contains(err.Error(), "spool") {
		t.Fatalf("err = %v", err)
	}
	if e.status().Enabled {
		t.Error("routing grant survived rollback")
	}
}

func TestEnableAppendsGitIgnoreInsideRepo(t *testing.T) {
	e := newEnv(t)
	e.gitRepo()
	e.startProxy()

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !strings.Contains(e.stdout.String(), ".gitignore") {
		t.Errorf("stdout = %q, want a .gitignore note", e.stdout)
	}
	ignore, err := os.ReadFile(filepath.Join(e.canonicalRoot(), ".gitignore"))
	if err != nil || !strings.Contains(string(ignore), claudesettings.ProjectLocalRel) {
		t.Errorf(".gitignore = %q, %v", ignore, err)
	}
}

func TestEnableGitIgnoresBundleArchiveAndUnpackedDirectory(t *testing.T) {
	e := newEnv(t)
	e.gitRepo()
	e.startProxy()

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	unpacked := filepath.Join(e.canonicalRoot(), "trajector-doctor-20260101-000000")
	if err := os.MkdirAll(unpacked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unpacked, "info.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"trajector-doctor-20260101-000000.tar.gz",
		"trajector-doctor-20260101-000000/info.json",
	} {
		if !e.gitIgnored(path) {
			t.Errorf("%s is not git-ignored after enable", path)
		}
	}
}

func TestEnableBackfillsUnpackedBundleRuleWithoutDuplicates(t *testing.T) {
	e := newEnv(t)
	e.gitRepo()
	ignorePath := filepath.Join(e.canonicalRoot(), ".gitignore")
	old := ".claude/settings.local.json\ntrajector-doctor-*.tar.gz\n"
	if err := os.WriteFile(ignorePath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	e.startProxy()

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	want := old + "trajector-doctor-*/\n"
	after, err := os.ReadFile(ignorePath)
	if err != nil || string(after) != want {
		t.Errorf(".gitignore = %q, %v, want %q", after, err, want)
	}

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("second enable: %v", err)
	}
	again, err := os.ReadFile(ignorePath)
	if err != nil || string(again) != want {
		t.Errorf(".gitignore after second enable = %q, %v, want %q", again, err, want)
	}
}

func TestEnableLeavesASymlinkedGitIgnoreAloneAndWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privilege on windows")
	}
	e := newEnv(t)
	e.gitRepo()
	target := filepath.Join(e.deps.Home, "dotfile")
	before := []byte("dotfile content\n")
	if err := os.WriteFile(target, before, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(e.canonicalRoot(), ".gitignore")); err != nil {
		t.Fatal(err)
	}
	e.startProxy()

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !strings.Contains(e.stderr.String(), "symbolic link") {
		t.Errorf("stderr = %q, want the symlinked .gitignore warned about", e.stderr)
	}
	after, err := os.ReadFile(target)
	if err != nil || string(after) != string(before) {
		t.Errorf("link target = %q, %v, want it untouched", after, err)
	}
}

func readOnly(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not block writes on windows")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
}

func TestEnableFailsWhenAcceptanceCannotBeRecorded(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	readOnly(t, filepath.Dir(e.layout().ConsentFile()))
	if err := e.machine().Enable(e.project, e.io()); err == nil {
		t.Fatal("enable succeeded without a writable consent store")
	}
}

func TestEnableRollsBackWhenRoutingTableUnwritable(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	consents := e.consentStore()
	if err := consents.AcceptAgreement(consent.AgreementVersion, "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	readOnly(t, filepath.Dir(e.layout().ConsentFile()))

	err := e.machine().Enable(e.project, e.io())
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(e.settingsPath()); !os.IsNotExist(err) {
		t.Error("injection survived rollback")
	}
}

func TestEnableFailsOnMalformedRoutingTable(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	consents := e.consentStore()
	if err := consents.AcceptAgreement(consent.AgreementVersion, "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	tablePath := e.layout().RoutingTable()
	if err := os.WriteFile(tablePath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.machine().Enable(e.project, e.io()); err == nil {
		t.Fatal("enable succeeded over a malformed routing table")
	}
	data, err := os.ReadFile(tablePath)
	if err != nil || string(data) != "{" {
		t.Errorf("routing table after rollback = %q, %v", data, err)
	}
}

func TestDisableFailsLoudlyWhenRevokeImpossible(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	readOnly(t, filepath.Dir(e.layout().ConsentFile()))

	err := e.machine().Disable(e.project, false, e.io())
	if err == nil || !strings.Contains(err.Error(), "revoking") {
		t.Fatalf("err = %v", err)
	}
}

func TestDisableHandlesInjectionWithoutRoute(t *testing.T) {
	e := newEnv(t)
	if err := claudesettings.InjectProject(
		e.settingsPath(),
		"http://127.0.0.1:41100/t/tok-orphan",
		e.deps.ExecPath+" hook ensure-proxy",
	); err != nil {
		t.Fatal(err)
	}

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, ok := claudesettings.InjectedBaseURL(e.settingsPath()); ok {
		t.Error("orphaned injection not removed")
	}
}
