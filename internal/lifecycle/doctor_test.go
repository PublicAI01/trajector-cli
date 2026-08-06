package lifecycle_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

func (e *env) doctor() (int, string) {
	e.t.Helper()
	problems, err := e.machine().Doctor(e.project, e.io())
	if err != nil {
		e.t.Fatalf("doctor: %v\nstdout: %s", err, e.stdout)
	}
	return problems, e.stdout.String()
}

func TestDoctorOnAHealthyEnabledProject(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	// A first pass may repair what enable does not own (the discovery
	// hint); the second pass must find nothing left to do.
	if _, err := e.machine().Doctor(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	e.stdout.Reset()
	problems, out := e.doctor()

	if problems != 0 {
		t.Fatalf("problems = %d on a healthy project, output:\n%s", problems, out)
	}
	if !strings.Contains(out, "Everything checks out") {
		t.Errorf("doctor = %q, want a clean summary", out)
	}
	if strings.Contains(out, "fixed") {
		t.Errorf("doctor = %q, want the second pass to have nothing to repair", out)
	}
}

func TestDoctorOnAFreshDeviceIsClean(t *testing.T) {
	e := newUnpairedEnv(t)
	problems, out := e.doctor()
	if problems != 0 {
		t.Fatalf("problems = %d on a fresh device, output:\n%s", problems, out)
	}
}

func TestDoctorRemovesAStaleInjection(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	// The grant is revoked behind the settings file's back, leaving an
	// injection that routes traffic on a token that no longer records.
	if err := routing.OpenStore(e.layout().RoutingTable()).Revoke(e.canonicalRoot(), "2026-08-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	e.stdout.Reset()
	problems, out := e.doctor()

	if problems != 0 {
		t.Fatalf("problems = %d, want the stale injection repaired, output:\n%s", problems, out)
	}
	if !strings.Contains(out, "fixed") {
		t.Errorf("doctor = %q, want the repair reported", out)
	}
	if _, injected := claudesettings.InjectedBaseURL(e.settingsPath()); injected {
		t.Error("stale injection still present after doctor")
	}
}

func TestDoctorRepairsMissingHooks(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	// The user deleted the hooks block; the injected base URL now routes
	// traffic with no session hook left to keep the proxy alive.
	data, err := os.ReadFile(e.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	delete(settings, "hooks")
	data, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.settingsPath(), data, 0o644); err != nil {
		t.Fatal(err)
	}

	e.stdout.Reset()
	problems, out := e.doctor()
	if problems != 0 {
		t.Fatalf("problems = %d, want the hooks repaired, output:\n%s", problems, out)
	}
	if !claudesettings.HasHook(e.settingsPath(), claudesettings.EnsureProxyMarker) {
		t.Error("ensure-proxy hooks still missing after doctor")
	}
}

func TestDoctorReportsAnOrphanedGrant(t *testing.T) {
	e := newEnv(t)
	e.sandbox.GrantProject(proxytest.Grant{
		Token:         "tok-orphaned-grant",
		ProjectIDHash: e.status().Hash,
		RootPath:      e.canonicalRoot(),
		Upstream:      "https://api.anthropic.com",
	})
	problems, out := e.doctor()

	if problems == 0 {
		t.Fatalf("problems = 0 for a grant with no injection, output:\n%s", out)
	}
	if !strings.Contains(out, "`trajector enable`") || !strings.Contains(out, "`trajector disable`") {
		t.Errorf("doctor = %q, want both ways out offered", out)
	}
}

func TestDoctorWarnsAboutAForeignPortHolder(t *testing.T) {
	e := newEnv(t)
	e.occupyPort()
	problems, out := e.doctor()

	if problems == 0 {
		t.Fatalf("problems = 0 with a foreign process on the port, output:\n%s", out)
	}
	if !strings.Contains(out, "not the trajector proxy") {
		t.Errorf("doctor = %q, want the foreign process called out", out)
	}
}

func TestDoctorFixesUpstreamDrift(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.environ["ANTHROPIC_BASE_URL"] = "https://relay-one.example.com"
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	e.environ["ANTHROPIC_BASE_URL"] = "https://relay-two.example.com"

	e.stdout.Reset()
	problems, out := e.doctor()
	if problems != 0 {
		t.Fatalf("problems = %d, want drift repaired, output:\n%s", problems, out)
	}
	if !strings.Contains(out, "https://relay-two.example.com") {
		t.Errorf("doctor = %q, want the new upstream reported", out)
	}
	grant, ok := e.sandbox.ActiveGrant(e.canonicalRoot())
	if !ok || grant.Upstream != "https://relay-two.example.com" {
		t.Errorf("grant upstream = %q after doctor, want the moved relay", grant.Upstream)
	}
}

func TestDoctorReinstallsTheDiscoveryHint(t *testing.T) {
	e := newEnv(t)
	userSettings := claudesettings.UserSettingsPath(e.deps.Home)
	if claudesettings.HasHook(userSettings, claudesettings.DiscoveryMarker) {
		t.Fatal("precondition: fresh env already has the discovery hook")
	}
	problems, out := e.doctor()

	if problems != 0 {
		t.Fatalf("problems = %d, want the hint reinstalled, output:\n%s", problems, out)
	}
	if !claudesettings.HasHook(userSettings, claudesettings.DiscoveryMarker) {
		t.Error("discovery hook still missing after doctor on a paired device")
	}
}

func TestDoctorListsRejectedBatches(t *testing.T) {
	e := newEnv(t)
	batchDir := filepath.Join(e.layout().RejectedDir(), "b-poison")
	if err := os.MkdirAll(batchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batchDir, "req-1.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reason, _ := json.Marshal(map[string]any{"batch_id": "b-poison", "records": 1, "details": "413 Request Entity Too Large"})
	if err := os.WriteFile(filepath.Join(batchDir, "reason.json"), reason, 0o600); err != nil {
		t.Fatal(err)
	}
	problems, out := e.doctor()

	if problems == 0 {
		t.Fatalf("problems = 0 with a quarantined batch, output:\n%s", out)
	}
	for _, want := range []string{"b-poison", "413 Request Entity Too Large", "requeue"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor = %q, want it to contain %q", out, want)
		}
	}
}

func TestDoctorWarnsWhenTheSpoolIsFull(t *testing.T) {
	e := newEnv(t)
	writeUploadFile(t, e, "handshake.json", map[string]any{"spool_quota_bytes": 1})
	e.sandbox.SeedRawcall("req-1", "hash-project", e.deps.Now())
	problems, out := e.doctor()

	if problems == 0 {
		t.Fatalf("problems = 0 with a full spool, output:\n%s", out)
	}
	if !strings.Contains(out, "full") {
		t.Errorf("doctor = %q, want the full spool called out", out)
	}
}
