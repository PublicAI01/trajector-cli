package lifecycle_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/apiproxy"
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
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

func TestDoctorExplainsADeviceWidePause(t *testing.T) {
	e := newEnv(t)
	e.sandbox.Pause(routing.PauseSignedOut)

	problems, out := e.doctor()
	if problems == 0 {
		t.Error("doctor found no problem on a paused device")
	}
	if !strings.Contains(out, "trajector login") {
		t.Errorf("doctor = %q, want the pause explained with its remedy", out)
	}
}

func TestDoctorEndsWithTheLiveProxyConfirmation(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	e.stdout.Reset()
	problems, out := e.doctor()
	if problems != 0 {
		t.Fatalf("problems = %d on a healthy project, output:\n%s", problems, out)
	}
	if !strings.Contains(out, "live proxy confirms") {
		t.Errorf("doctor = %q, want the live proxy's own confirmation", out)
	}
}

func TestDoctorFlagsALiveProxyThatWillNotRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permissions work differently on Windows")
	}
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	// Every file is consistent, but the machine changed under the live
	// proxy: its spool no longer accepts writes. Only asking the proxy
	// itself — the same self-check enable runs — can surface that.
	spoolDir := e.layout().SpoolDir()
	if err := os.Chmod(spoolDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(spoolDir, 0o700) })

	e.stdout.Reset()
	problems, out := e.doctor()
	if problems == 0 {
		t.Fatalf("problems = 0 while the live proxy cannot record, output:\n%s", out)
	}
	if !strings.Contains(out, "live proxy will not record") {
		t.Errorf("doctor = %q, want the live proxy's refusal reported", out)
	}
}

func TestDoctorReportsAnUnreadableTokenStore(t *testing.T) {
	e := newEnv(t)
	// Make the stored token unreadable (not absent): the pairing state
	// is now unknown, which must never present as signed out.
	secret := filepath.Join(e.layout().SecretsDir(), "device.secret")
	if err := os.Remove(secret); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(secret, 0o700); err != nil {
		t.Fatal(err)
	}

	problems, out := e.doctor()
	if problems == 0 {
		t.Error("doctor found no problem with an unreadable token store")
	}
	if !strings.Contains(out, "token store could not be read") {
		t.Errorf("doctor = %q, want the unreadable token store named", out)
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

func TestDoctorPresentsAnUnverifiableProxyAsAuthentication(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	e.proxyEnv.AdminToken()
	proxytest.RemoveAdminTokens(t, e.layout(), e.proxyEnv.Addr())
	problems, out := e.doctor()

	if problems == 0 {
		t.Fatalf("problems = 0 with an unverifiable proxy on the port, output:\n%s", out)
	}
	if !strings.Contains(out, "could not verify the proxy") || !strings.Contains(out, "authentication problem") {
		t.Errorf("doctor = %q, want the authentication problem explained", out)
	}
	if strings.Contains(out, "find and stop the process") {
		t.Errorf("doctor = %q, must not advise hunting a process that may be our own proxy", out)
	}
}

func TestDoctorWarnsAboutAHealthzCopyingPortHolder(t *testing.T) {
	e := newEnv(t)
	im := e.occupyPortWithHealthzCopy()
	problems, out := e.doctor()

	if problems == 0 {
		t.Fatalf("problems = 0 with a health-copying holder on the port, output:\n%s", out)
	}
	if !strings.Contains(out, "not the trajector proxy") {
		t.Errorf("doctor = %q, want the unproven holder called out", out)
	}
	if im.SawHeader(apiproxy.AdminHeader) {
		t.Error("the admin token was sent to a holder that never proved it knows it")
	}
}

func TestDoctorLeavesANewerProxyServing(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "1.0.0"
	e.startProxy(proxytest.WithVersion("2.0.0"))
	problems, out := e.doctor()

	if problems != 0 {
		t.Fatalf("problems = %d with a newer proxy on the port, output:\n%s", problems, out)
	}
	if strings.Contains(out, "replaced the version") {
		t.Errorf("doctor = %q, want the newer proxy left alone", out)
	}
	if !strings.Contains(out, proxylife.ReuseReason) {
		t.Errorf("doctor = %q, want the takeover rule stated", out)
	}
	if got := e.proxyEnv.Healthz().Version; got != "2.0.0" {
		t.Errorf("proxy version after doctor = %q, want the newer proxy still serving", got)
	}
}

func TestDoctorLeavesAProxyItCannotOrderAgainstServing(t *testing.T) {
	e := newEnv(t) // this build announces "testv", which no order covers
	e.startProxy(proxytest.WithVersion("1.2.3"))
	problems, out := e.doctor()

	if problems != 0 {
		t.Fatalf("problems = %d with an unordered version pair, output:\n%s", problems, out)
	}
	if !strings.Contains(out, proxylife.ReuseReason) {
		t.Errorf("doctor = %q, want the takeover rule stated", out)
	}
	if got := e.proxyEnv.Healthz().Version; got != "1.2.3" {
		t.Errorf("proxy version after doctor = %q, want the release proxy still serving", got)
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
	for _, want := range []string{"b-poison", "413 Request Entity Too Large", "requeue", "discard"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor = %q, want it to contain %q", out, want)
		}
	}
}

func TestDoctorRelaysTheServiceHandshakeWithoutCallingItAFault(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "0.1.0" // behind the minimum below
	writeUploadFile(t, e, "handshake.json", map[string]any{
		"min_client_version": "9.9.9",
		"notice":             "maintenance on Friday",
	})
	problems, out := e.doctor()

	// Being behind the service is not a broken machine: both fields are
	// relayed and neither moves the exit code.
	if problems != 0 {
		t.Fatalf("problems = %d, want the handshake relayed without affecting the exit code, output:\n%s", problems, out)
	}
	for _, want := range []string{"9.9.9", "0.1.0", "maintenance on Friday", "trajector upgrade"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor = %q, want it to contain %q", out, want)
		}
	}
}

// doctor answers the same question status does, from the same
// judgement — a build that meets the service's minimum is told nothing
// about versions on either surface. Two spellings of this rule would
// eventually disagree, and the user would have no way to know which
// one to believe.
func TestDoctorRelaysAPausedUploaderWithoutCallingTheMachineBroken(t *testing.T) {
	// Nothing here is broken and nothing doctor can do would change it:
	// the user finishes this in a browser. Counting it as a problem would
	// fail `trajector doctor` on a healthy install.
	e := newEnv(t)
	e.deps.Version = "0.1.0"
	writeUploadFile(t, e, "handshake.json", map[string]any{
		"authorization_required": true,
		"authorize_url":          "https://dashboard.example.com/authorization",
		"authorization_message":  "Your data authorization is not complete.",
	})
	problems, out := e.doctor()

	if problems != 0 {
		t.Fatalf("problems = %d, want a paused uploader reported without failing doctor, output:\n%s", problems, out)
	}
	for _, want := range []string{
		"data authorization is not complete",
		"Your data authorization is not complete.",
		"https://dashboard.example.com/authorization",
		"Captured data is kept",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor = %q, want it to contain %q", out, want)
		}
	}
}

func TestDoctorSaysNothingAboutAMinimumThisBuildMeets(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "0.1.0"
	writeUploadFile(t, e, "handshake.json", map[string]any{
		"min_client_version": "0.1.0",
		"notice":             "maintenance on Friday",
	})
	problems, out := e.doctor()

	if problems != 0 {
		t.Fatalf("problems = %d, want a compliant build reported clean, output:\n%s", problems, out)
	}
	if !strings.Contains(out, "maintenance on Friday") {
		t.Errorf("doctor = %q, want the notice still relayed", out)
	}
	for _, unwanted := range []string{"trajector upgrade", "requires client version"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("doctor = %q, want no %q for a build that meets the minimum", out, unwanted)
		}
	}
}

func TestDoctorStatesAnUnorderableMinimumWithoutSendingTheUserToUpgrade(t *testing.T) {
	e := newEnv(t) // this build announces "testv", which no order covers
	writeUploadFile(t, e, "handshake.json", map[string]any{"min_client_version": "9.9.9"})
	problems, out := e.doctor()

	if problems != 0 {
		t.Fatalf("problems = %d on an unorderable version pair, output:\n%s", problems, out)
	}
	for _, want := range []string{"9.9.9", "testv"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor = %q, want both versions stated", out)
		}
	}
	if strings.Contains(out, "trajector upgrade") {
		t.Errorf("doctor = %q, want no upgrade instruction on an unorderable pair", out)
	}
}

func TestDoctorRelaysWhatTheServiceSaidAboutTheVersion(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "0.1.0"
	writeUploadFile(t, e, "handshake.json", map[string]any{
		"min_client_version": "9.9.9",
		"upgrade_message":    "Upload format 0.1.x is retired on 2026-09-01.",
	})
	problems, out := e.doctor()

	// Being behind the service is not a broken machine; doctor reports
	// it and says what to run, without claiming there is a fault.
	if problems != 0 {
		t.Fatalf("problems = %d, want a version refusal relayed without an exit code, output:\n%s", problems, out)
	}
	for _, want := range []string{"the service says:", "retired on 2026-09-01", "trajector upgrade"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor = %q, want it to contain %q", out, want)
		}
	}
}

func TestStatusAndDoctorPresentAnUnusableSpoolAlike(t *testing.T) {
	e := newEnv(t)
	e.obstruct(e.layout().SpoolDir())
	want := "the capture spool at " + e.layout().SpoolDir() + " is not usable"

	statusOut := e.statusOutput()
	e.stdout.Reset()
	problems, doctorOut := e.doctor()

	if problems == 0 {
		t.Fatalf("problems = 0 with an unusable spool, output:\n%s", doctorOut)
	}
	for surface, out := range map[string]string{"status": statusOut, "doctor": doctorOut} {
		if !strings.Contains(out, want) {
			t.Errorf("%s = %q, want it to contain %q", surface, out, want)
		}
	}
}

func TestDoctorReportsRejectedBatchesItCannotRead(t *testing.T) {
	e := newEnv(t)
	e.obstruct(e.layout().RejectedDir())
	problems, out := e.doctor()

	if problems == 0 {
		t.Fatalf("problems = 0 with an unreadable quarantine, output:\n%s", out)
	}
	if want := "the rejected batches at " + e.layout().RejectedDir() + " could not be read"; !strings.Contains(out, want) {
		t.Errorf("doctor = %q, want it to contain %q", out, want)
	}
}

func TestStatusAndDoctorPresentAFullSpoolAlike(t *testing.T) {
	e := newEnv(t)
	writeUploadFile(t, e, "handshake.json", map[string]any{"spool_quota_bytes": 1})
	e.sandbox.SeedRawcall("req-1", "hash-project", e.deps.Now())

	statusOut := e.statusOutput()
	e.stdout.Reset()
	problems, doctorOut := e.doctor()

	if problems == 0 {
		t.Fatalf("problems = 0 with a full spool, output:\n%s", doctorOut)
	}
	for surface, out := range map[string]string{"status": statusOut, "doctor": doctorOut} {
		if !strings.Contains(out, "not writable, so recording is stopped") {
			t.Errorf("%s = %q, want the stopped-recording sentence", surface, out)
		}
		if !strings.Contains(out, "The spool is full. Run `trajector upload --force`") {
			t.Errorf("%s = %q, want the full-spool remedy", surface, out)
		}
	}
}

func TestStatusAndDoctorPresentAnUnwritableSpoolAlike(t *testing.T) {
	e := newEnv(t)
	readOnly(t, e.layout().SpoolDir())

	statusOut := e.statusOutput()
	e.stdout.Reset()
	problems, doctorOut := e.doctor()

	if problems == 0 {
		t.Fatalf("problems = 0 with an unwritable spool, output:\n%s", doctorOut)
	}
	for surface, out := range map[string]string{"status": statusOut, "doctor": doctorOut} {
		if !strings.Contains(out, "not writable, so recording is stopped") {
			t.Errorf("%s = %q, want the stopped-recording sentence", surface, out)
		}
		if strings.Contains(out, "The spool is full") {
			t.Errorf("%s = %q, want no quota remedy for a spool that is not full", surface, out)
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
