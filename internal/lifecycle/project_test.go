package lifecycle_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

func TestEnableInjectsRoutesAndSelfChecks(t *testing.T) {
	e := newEnv(t)
	e.startProxy()

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v\nstdout: %s", err, e.stdout)
	}

	st := e.status()
	if !st.Enabled {
		t.Fatal("no active route after enable")
	}
	if len(st.Token) != 32 {
		t.Errorf("token %q is not a 128-bit hex value", st.Token)
	}
	if st.Upstream != "https://api.anthropic.com" {
		t.Errorf("upstream = %q", st.Upstream)
	}
	if st.GrantHash != st.Hash {
		t.Error("route hash does not match the project hash")
	}
	if st.InjectedBaseURL == "" {
		t.Fatal("no injected base URL")
	}
	if st.InjectedToken != st.Token {
		t.Error("injected token differs from the routed token")
	}
	if !st.HookInstalled {
		t.Error("ensure-proxy hooks missing")
	}
	if st.AgreementVersion != consent.AgreementVersion {
		t.Errorf("accepted agreement = %q", st.AgreementVersion)
	}
	if st.ConsentState != consent.StateGranted {
		t.Errorf("project consent = %q", st.ConsentState)
	}

	if !strings.Contains(e.stdout.String(), "Self-check passed") {
		t.Errorf("stdout = %q", e.stdout)
	}
}

func TestEnableIsIdempotentAndKeepsToken(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	first := e.status()

	e.stdin = ""
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("second enable: %v", err)
	}
	second := e.status()
	if !second.Enabled || second.Token != first.Token {
		t.Errorf("token changed on re-enable: %q -> %q", first.Token, second.Token)
	}

	var settings map[string]any
	data, err := os.ReadFile(e.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]any)
	if groups := hooks["SessionStart"].([]any); len(groups) != 1 {
		t.Errorf("SessionStart groups = %d after re-enable, want 1", len(groups))
	}
}

func TestEnableDeclinedLeavesNoTrace(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.stdin = "no\n"

	err := e.machine().Enable(e.project, e.io())
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("err = %v, want declined", err)
	}
	if _, err := os.Stat(e.settingsPath()); !os.IsNotExist(err) {
		t.Error("settings file created despite declined agreement")
	}
	if e.status().Enabled {
		t.Error("route granted despite declined agreement")
	}
}

func TestEnableSkipsPromptWhenAlreadyAccepted(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	consents := e.consentStore()
	if err := consents.AcceptAgreement(consent.AgreementVersion, "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	e.stdin = ""

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if strings.Contains(e.stdout.String(), "[yes/no]") {
		t.Error("agreement prompt shown despite prior acceptance")
	}
}

func TestEnableStaleAgreementRepromptsAndResumesCapture(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	consents := e.consentStore()
	if err := consents.AcceptAgreement("2020-01-obsolete", "2020-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	e.sandbox.Pause(routing.PauseConsentReconfirm)

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "agreement changed") {
		t.Errorf("stdout = %q", e.stdout)
	}
	version, _, _ := consents.AcceptedVersion()
	if version != consent.AgreementVersion {
		t.Errorf("accepted version = %q", version)
	}
	if reason := e.sandbox.PausedReason(); reason != "" {
		t.Errorf("pause %q still active after reconfirmation", reason)
	}
}

func TestEnableRefusesBedrockChannel(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.environ["CLAUDE_CODE_USE_BEDROCK"] = "1"

	err := e.machine().Enable(e.project, e.io())
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(e.settingsPath()); !os.IsNotExist(err) {
		t.Error("injection happened despite unsupported channel")
	}
}

func TestEnableChainsThirdPartyUpstream(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.environ["ANTHROPIC_BASE_URL"] = "https://relay.example.com"

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	st := e.status()
	if !st.Enabled || st.Upstream != "https://relay.example.com" {
		t.Errorf("status = %+v", st)
	}
	if !strings.Contains(e.stdout.String(), "third-party") {
		t.Errorf("no third-party notice in output: %q", e.stdout)
	}
}

func TestEnableRollsBackWhenPortIsForeign(t *testing.T) {
	e := newEnv(t)
	e.occupyPort()

	err := e.machine().Enable(e.project, e.io())
	if err == nil || !strings.Contains(err.Error(), "not the trajector proxy") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("err = %v, want rollback notice", err)
	}
	if _, err := os.Stat(e.settingsPath()); !os.IsNotExist(err) {
		t.Error("settings injection survived rollback")
	}
	st := e.status()
	if st.Enabled {
		t.Error("routing grant survived rollback")
	}
	if st.ConsentState != "" {
		t.Error("project consent record survived rollback")
	}
	if st.AgreementVersion != consent.AgreementVersion {
		t.Error("agreement acceptance must survive rollback: the user did accept it")
	}
}

func TestEnableRollbackRestoresUserSettingsBytes(t *testing.T) {
	e := newEnv(t)
	e.occupyPort()
	if err := os.MkdirAll(filepath.Dir(e.settingsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "{\n  \"env\": {\"MY_VAR\": \"keep\"}\n}\n"
	if err := os.WriteFile(e.settingsPath(), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := e.machine().Enable(e.project, e.io()); err == nil {
		t.Fatal("enable succeeded against a foreign port")
	}
	data, err := os.ReadFile(e.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("settings after rollback = %q, want original bytes", data)
	}
}

func TestEnableRollsBackWhenProxyCannotStart(t *testing.T) {
	e := newEnv(t)
	// Nothing listens on the proxy address and the exec path does not
	// exist, so ensure-proxy cannot bring one up.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.deps.ProxyAddr = l.Addr().String()
	l.Close()

	if err := e.machine().Enable(e.project, e.io()); err == nil {
		t.Fatal("enable succeeded without a startable proxy")
	}
	if _, err := os.Stat(e.settingsPath()); !os.IsNotExist(err) {
		t.Error("settings injection survived rollback")
	}
	if e.status().Enabled {
		t.Error("routing grant survived rollback")
	}
}

func TestDisableRemovesInjectionRevokesAndDeletesProjectData(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	route := e.status()
	seeded := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)
	e.sandbox.SeedRawcall("req-mine", route.GrantHash, seeded)
	e.sandbox.SeedRawcall("req-other", "hash-other-project", seeded)

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}

	after := e.status()
	if after.InjectedBaseURL != "" {
		t.Error("base URL still injected")
	}
	if after.HookInstalled {
		t.Error("hooks still injected")
	}
	if after.Enabled {
		t.Error("route still active")
	}
	if known, recording := e.sandbox.Recording(route.Token); !known || recording {
		t.Error("revoked token must stay resolvable for forwarding, with recording off")
	}
	if after.ConsentState != consent.StateDenied {
		t.Errorf("consent state = %q, want denied", after.ConsentState)
	}

	remaining := e.sandbox.ProjectsWithRawcalls()
	if remaining[route.GrantHash] != 0 {
		t.Error("this project's rawcalls not deleted")
	}
	if remaining["hash-other-project"] != 1 {
		t.Errorf("another project's rawcalls changed: %v", remaining)
	}
}

func TestDisableWithoutEnableIsCleanNoop(t *testing.T) {
	e := newEnv(t)
	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable on never-enabled project: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "not enabled") {
		t.Errorf("stdout = %q", e.stdout)
	}
}

func TestDisableTwiceIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatal(err)
	}
	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("second disable: %v", err)
	}
}

func TestDisableAlsoDeletesRejectedRawcallsOfTheProject(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	route := e.status()

	batchDir := filepath.Join(e.layout().RejectedDir(), "b-test")
	if err := os.MkdirAll(batchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, projectIDHash string) {
		data, err := json.Marshal(map[string]any{
			"request_id": name,
			"capture":    map[string]any{"project_id_hash": projectIDHash},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(batchDir, name+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("req-mine", route.GrantHash)
	write("req-other", "hash-other-project")

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}

	if _, err := os.Stat(filepath.Join(batchDir, "req-mine.json")); !os.IsNotExist(err) {
		t.Error("this project's rejected rawcall survived disable")
	}
	if _, err := os.Stat(filepath.Join(batchDir, "req-other.json")); err != nil {
		t.Errorf("another project's rejected rawcall was touched: %v", err)
	}
	// The deletion count must split its sources: a user deciding what to
	// do about quarantined data needs to see that disable reached it.
	if out := e.stdout.String(); !strings.Contains(out, "0 from the spool, 1 from rejected batches") {
		t.Errorf("stdout = %q, want the deletion count split by source", out)
	}
}

func TestDisableSplitsTheDeletionCountBySource(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	route := e.status()
	e.sandbox.SeedRawcall("req-mine", route.GrantHash, time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC))
	seedRejectedBatch(t, e, "b-poison", "", map[string][]byte{
		"req-rejected": rejectedRecordFor(t, route.GrantHash),
	})

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if out := e.stdout.String(); !strings.Contains(out, "Deleted 2 unuploaded rawcall(s) for this project (1 from the spool, 1 from rejected batches).") {
		t.Errorf("stdout = %q, want the deletion count split by source", out)
	}
}

// rejectedRecordFor builds valid rawcall bytes belonging to a project,
// as a quarantined record would hold them.
func rejectedRecordFor(t *testing.T, projectIDHash string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"request_id": "req-rejected",
		"capture":    map[string]any{"project_id_hash": projectIDHash},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestDisableRerunFinishesAnInterruptedWithdrawal pins that a disable
// interrupted after the grant is revoked can be finished by rerunning
// it. Traffic is stopped before records are deleted, so a failure in
// between leaves a project that looks untouched to the early "nothing to
// do" check while its rawcalls are still on disk — and the uploader does
// not consult consent, so they would ship on the next flush.
func TestDisableRerunFinishesAnInterruptedWithdrawal(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	route := e.status()
	seedRejectedBatch(t, e, "b-poison", "", map[string][]byte{
		"req-rejected": rejectedRecordFor(t, route.GrantHash),
	})

	// Break the spool so the first disable fails at its deletion step,
	// after it has already removed the injection and revoked the grant.
	e.obstruct(e.layout().SpoolDir())
	if err := e.machine().Disable(e.project, false, e.io()); err == nil {
		t.Fatal("precondition: the first disable must fail at the deletion step")
	}
	if st := e.status(); st.Injected() || st.Enabled {
		t.Fatal("precondition: the first disable must have removed the injection and revoked the grant")
	}
	if err := os.Remove(e.layout().SpoolDir()); err != nil {
		t.Fatal(err)
	}
	e.stdout.Reset()

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("rerun: %v\nstdout: %s", err, e.stdout)
	}
	record := filepath.Join(e.layout().RejectedDir(), "b-poison", "req-rejected.json")
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("a withdrawn project's quarantined rawcall survived the rerun (stat: %v)\nstdout: %s", err, e.stdout)
	}
}

// TestEnableAndDisableKeepAUsersOwnBaseURL pins both halves of one
// defect. Injection writes ANTHROPIC_BASE_URL into the project-local
// settings file, which is also the first link of the configuration
// chain, so a relay configured there is overwritten on the way in. That
// made re-running enable re-grant the official endpoint under it, and
// made disable delete the last copy of the value — either way the next
// session went to the official endpoint carrying relay credentials.
// relayInSettingsLocal is what every displaced-base-URL test starts
// from: the user keeps their own relay in the project-local settings
// file, which is also the file — and the key — enable injects into, so
// enabling overwrites the last copy of it.
const relayInSettingsLocal = "https://relay.example.com"

func enabledOverAUsersOwnRelay(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.startProxy()
	if err := os.MkdirAll(filepath.Dir(e.settingsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.settingsPath(), []byte(`{"env":{"ANTHROPIC_BASE_URL":"`+relayInSettingsLocal+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v\nstdout: %s", err, e.stdout)
	}
	if got := e.status().Upstream; got != relayInSettingsLocal {
		t.Fatalf("test setup: grant routes at %q, want the user's own %q", got, relayInSettingsLocal)
	}
	return e
}

// ownBaseURL reports the base URL the project's configuration chain
// names once trajector's own injection is out of the way.
func ownBaseURL(t *testing.T, e *env) string {
	t.Helper()
	value, _, _ := claudesettings.ExternalBaseURL(e.canonicalRoot(), e.deps.Home, e.deps.Getenv)
	return value
}

// TestUninstallPutsBackAUsersOwnBaseURL is the uninstall half of what
// TestEnableAndDisableKeepAUsersOwnBaseURL pins for disable. Removal
// deletes exactly what trajector wrote, which for a relay kept in the
// project-local settings file is the last copy of the user's own value.
// Until 2026-08-21 only disable put it back, so `trajector uninstall`
// left the project with no base URL at all and the user's next session
// carried their relay's credentials to the official endpoint.
func TestUninstallPutsBackAUsersOwnBaseURL(t *testing.T) {
	e := enabledOverAUsersOwnRelay(t)

	if err := e.machine().Uninstall(false, e.io()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got := ownBaseURL(t, e); got != relayInSettingsLocal {
		t.Errorf("after uninstall the project's own base URL is %q, want %q back", got, relayInSettingsLocal)
	}
}

// TestUninstallDoesNotWriteABaseURLIntoAProjectItNeverDisplacedOneIn is
// the other half of the restore rule. removeInjection took "the grant
// names a relay and the configuration chain is quiet now" as proof that
// we had displaced that relay out of the project-local settings file. It
// is not: a relay the user exports from their shell was never displaced
// in any file, and a shell that does not export it in this terminal is
// quiet for a reason of its own. Once uninstall started coming through
// removeInjection on 2026-08-21 it walked every root the routing table
// ever held and wrote that relay into projects it had nothing injected
// in — creating .claude/settings.local.json, and through MkdirAll a whole
// project tree the user had deleted, in the one command whose job is to
// take our files back out.
func TestUninstallDoesNotWriteABaseURLIntoAProjectItNeverDisplacedOneIn(t *testing.T) {
	const shellRelay = "https://relay-from-the-shell.example.com"
	e := newEnv(t)
	e.startProxy()
	// The user's relay lives in their shell, so enable records it without
	// overwriting anything of theirs in a file.
	e.environ["ANTHROPIC_BASE_URL"] = shellRelay
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v\nstdout: %s", err, e.stdout)
	}
	if got := e.status().Upstream; got != shellRelay {
		t.Fatalf("test setup: grant routes at %q, want the shell's %q", got, shellRelay)
	}
	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v\nstdout: %s", err, e.stdout)
	}
	// A grant whose project directory the user has since deleted:
	// uninstall walks it too, and nothing may recreate the tree.
	gone := filepath.Join(t.TempDir(), "deleted-project")
	if err := routing.OpenStore(e.layout().RoutingTable()).Grant(routing.Grant{
		Token: "tok-gone", ProjectIDHash: "hash-gone", RootPath: gone,
		Upstream: shellRelay, GrantedAt: "2026-08-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	// Uninstall runs from a terminal that is not the one exporting the relay.
	delete(e.environ, "ANTHROPIC_BASE_URL")

	if err := e.machine().Uninstall(false, e.io()); err != nil {
		t.Fatalf("uninstall: %v\nstdout: %s", err, e.stdout)
	}

	if got := ownBaseURL(t, e); got != "" {
		t.Errorf("uninstall left the project naming %q as its own base URL; it never displaced one here", got)
	}
	if data, err := os.ReadFile(e.settingsPath()); err == nil && strings.Contains(string(data), shellRelay) {
		t.Errorf("uninstall wrote the shell's relay into %s:\n%s", e.settingsPath(), data)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("uninstall recreated the deleted project at %s (stat: %v)", gone, err)
	}
}

// TestDoctorPutsBackAUsersOwnBaseURLWithAStaleInjection is the same
// guarantee for doctor's stale-injection repair, which reaches an
// injection whose grant is already revoked — so the revoked entry is by
// then the only surviving record of the displaced value.
func TestDoctorPutsBackAUsersOwnBaseURLWithAStaleInjection(t *testing.T) {
	e := enabledOverAUsersOwnRelay(t)
	// Revoke without removing the injection, the half-withdrawn state
	// doctor exists to reconcile.
	if err := routing.OpenStore(e.layout().RoutingTable()).Revoke(e.canonicalRoot(), "2026-08-21T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	if _, err := e.machine().Doctor(e.project, e.io()); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if st := e.status(); st.Injected() {
		t.Fatalf("doctor left the stale injection %q in place", st.InjectedBaseURL)
	}
	if got := ownBaseURL(t, e); got != relayInSettingsLocal {
		t.Errorf("after doctor removed the stale injection the project's own base URL is %q, want %q back", got, relayInSettingsLocal)
	}
}

func TestEnableAndDisableKeepAUsersOwnBaseURL(t *testing.T) {
	e := enabledOverAUsersOwnRelay(t)
	// Re-running enable repairs rather than re-keys, the relay included.
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("re-enable: %v\nstdout: %s", err, e.stdout)
	}
	if got := e.status().Upstream; got != relayInSettingsLocal {
		t.Fatalf("after re-enable the grant routes at %q, want the user's own %q", got, relayInSettingsLocal)
	}

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := ownBaseURL(t, e); got != relayInSettingsLocal {
		t.Errorf("after disable the user's own base URL is %q, want %q back", got, relayInSettingsLocal)
	}
}

// TestDisableInsideASessionNamesTheBaseURLItCouldNotPutBack is the
// masked half of the restore rule TestEnableAndDisableKeepAUsersOwnBaseURL
// pins. Run from inside a Claude Code session — which is where a user
// asks their agent to turn trajector off — the configuration chain reads
// as masked, because that session exported our own injected value to
// every process it spawns. Removal therefore cannot tell whether it
// displaced anything in this file and must not guess. Until 2026-08-25
// it also said nothing: the relay went with the injection, rerunning
// disable took the purge-only path and never came back through here, and
// the user's next session carried their relay's credentials to the
// official endpoint with no sign anywhere that a setting had been lost.
func TestDisableInsideASessionNamesTheBaseURLItCouldNotPutBack(t *testing.T) {
	e := enabledOverAUsersOwnRelay(t)
	// The session's environment carries trajector's own injected base URL.
	e.environ["ANTHROPIC_BASE_URL"] = e.status().InjectedBaseURL

	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v\nstdout: %s", err, e.stdout)
	}
	if !strings.Contains(e.stderr.String(), relayInSettingsLocal) {
		t.Errorf("disable dropped the user's own base URL %q without naming it:\nstdout: %s\nstderr: %s",
			relayInSettingsLocal, e.stdout, e.stderr)
	}
}
