package lifecycle_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
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
	e.sandbox.Pause(proxytest.PausedConsentReconfirm)

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
