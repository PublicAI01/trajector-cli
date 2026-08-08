package lifecycle_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

func TestLoginPairsStoresTheTokenAndResumesRecording(t *testing.T) {
	e := newUnpairedEnv(t)
	e.pairable()
	e.sandbox.Pause(routing.PauseSignedOut)

	if err := e.machine().Login(e.io()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !e.machine().Paired() {
		t.Error("device not paired after login")
	}
	if reason := e.sandbox.PausedReason(); reason != "" {
		t.Errorf("pause %q survived login", reason)
	}
	if !claudesettings.HasHook(claudesettings.UserSettingsPath(e.deps.Home), claudesettings.DiscoveryMarker) {
		t.Error("discovery hint not installed")
	}
	if !strings.Contains(e.stdout.String(), "example.com/pair") {
		t.Errorf("stdout = %q, want the verification link", e.stdout)
	}
}

func TestLoginOnAlreadyPairedDeviceStillReachesTheSignedInState(t *testing.T) {
	e := newEnv(t)
	e.sandbox.Pause(routing.PauseSignedOut)

	if err := e.machine().Login(e.io()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "already paired") {
		t.Errorf("stdout = %q", e.stdout)
	}
	if reason := e.sandbox.PausedReason(); reason != "" {
		t.Errorf("pause %q survived a re-login", reason)
	}
}

func TestLoginRidesOutATransientPairingCheckFailure(t *testing.T) {
	e := newUnpairedEnv(t)
	e.service.PairableAsAfterOutage("pair-1", "dev-tok-fake")

	if err := e.machine().Login(e.io()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !e.machine().Paired() {
		t.Error("device not paired after login")
	}
}

func TestLoginKeepsPollingThroughAnOutageUntilTheWindowCloses(t *testing.T) {
	e := newUnpairedEnv(t)
	e.service.PairingOutage("pair-1")
	now := e.deps.Now()
	e.deps.Now = func() time.Time {
		now = now.Add(time.Minute)
		return now
	}

	err := e.machine().Login(e.io())
	if err == nil || !strings.Contains(err.Error(), "timed out waiting for approval") {
		t.Fatalf("login = %v, want the approval window reported closed", err)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("login = %v, want the last failed check named", err)
	}
	checks := 0
	for _, r := range e.service.Requests() {
		if r.Method == "GET" {
			checks++
		}
	}
	if checks < 2 {
		t.Errorf("service saw %d status checks, want polling to continue past the first failure", checks)
	}
	if e.machine().Paired() {
		t.Error("device paired despite a service outage")
	}
}

func TestLoginStopsWhenTheServiceNoLongerKnowsThePairing(t *testing.T) {
	e := newUnpairedEnv(t)
	e.service.PairingVanishes("pair-1")

	err := e.machine().Login(e.io())
	if err == nil || !strings.Contains(err.Error(), "checking pairing") {
		t.Fatalf("login = %v, want the refused check surfaced", err)
	}
	checks := 0
	for _, r := range e.service.Requests() {
		if r.Method == "GET" {
			checks++
		}
	}
	if checks != 1 {
		t.Errorf("service saw %d status checks, want no retry of a positively refused pairing", checks)
	}
	if e.machine().Paired() {
		t.Error("device paired despite a refused pairing check")
	}
}

func TestLoginReportsAnExpiredPairingLink(t *testing.T) {
	e := newUnpairedEnv(t)
	e.service.PairingExpires("pair-1")

	err := e.machine().Login(e.io())
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("login = %v, want the expired link reported", err)
	}
	if e.machine().Paired() {
		t.Error("device paired despite an expired link")
	}
}

func TestLoginSurfacesAPairingStartFailure(t *testing.T) {
	e := newUnpairedEnv(t)
	e.service.Stub("POST", "/v1/pairings", fakeplatform.JSON(500, map[string]any{"error": "down"}))

	if err := e.machine().Login(e.io()); err == nil || !strings.Contains(err.Error(), "starting pairing") {
		t.Errorf("login with a failing pairing start = %v, want the start failure surfaced", err)
	}
}

func TestLogoutRevokesPausesAndKeepsGrants(t *testing.T) {
	e := newEnv(t)
	e.service.Stub("POST", "/v1/device/revoke", fakeplatform.JSON(200, map[string]any{}))
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	grant := e.status()
	if !grant.Enabled {
		t.Fatal("project not enabled")
	}

	if err := e.machine().Logout(e.io()); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if e.machine().Paired() {
		t.Error("device token survived logout")
	}
	reqs := e.service.Requests()
	last := reqs[len(reqs)-1]
	if last.URL != "/v1/device/revoke" || last.Header.Get("Authorization") != "Bearer dev-tok-fake" {
		t.Errorf("revocation request = %+v", last)
	}
	if reason := e.sandbox.PausedReason(); reason != routing.PauseSignedOut {
		t.Errorf("pause = %q, want signed_out", reason)
	}
	if known, recording := e.sandbox.Recording(grant.Token); !known || recording {
		t.Error("signing out must keep grants resolvable for forwarding, with recording off")
	}
}

func TestLogoutWithAnUnreachableServiceStillSignsOutLocally(t *testing.T) {
	e := newEnv(t)
	// No stub for revoke, so the service answers with a loud failure.
	if err := e.machine().Logout(e.io()); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(e.stderr.String(), "account page") {
		t.Errorf("stderr = %q, want a manual-revocation warning", e.stderr)
	}
	if e.machine().Paired() {
		t.Error("device token kept after an offline logout")
	}
	if reason := e.sandbox.PausedReason(); reason != routing.PauseSignedOut {
		t.Errorf("pause = %q", reason)
	}
}

func TestLogoutTellsAnAlreadyRevokedTokenFromAServiceOutage(t *testing.T) {
	t.Run("an already-revoked token is the goal state, not a warning", func(t *testing.T) {
		e := newEnv(t)
		e.service.Stub("POST", "/v1/device/revoke", fakeplatform.JSON(401, map[string]any{"error": "unknown token"}))
		if err := e.machine().Logout(e.io()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(e.stderr.String(), "account page") {
			t.Errorf("stderr = %q, want no manual-revocation advice for a token that is already gone", e.stderr)
		}
	})
	t.Run("a service outage advises retrying later", func(t *testing.T) {
		e := newEnv(t)
		e.service.Stub("POST", "/v1/device/revoke", fakeplatform.JSON(503, map[string]any{"error": "down"}))
		if err := e.machine().Logout(e.io()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(e.stderr.String(), "retry `trajector logout` later") {
			t.Errorf("stderr = %q, want retry-later guidance", e.stderr)
		}
	})
}

func TestPurgeRefusedByTheServiceDoesNotAdviseRetrying(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	e.service.Stub("POST", "/v1/data-deletions", fakeplatform.JSON(400, map[string]any{"error": "malformed"}))

	err := e.machine().Disable(e.project, true, e.io())
	if err == nil {
		t.Fatal("a refused deletion request reported success")
	}
	if strings.Contains(err.Error(), "retry with") {
		t.Errorf("err = %v, want no retry advice for a request that cannot succeed", err)
	}
	if !strings.Contains(err.Error(), "account page") {
		t.Errorf("err = %v, want the account-page fallback", err)
	}
}

func TestLogoutWhenNotPairedIsANoop(t *testing.T) {
	e := newUnpairedEnv(t)
	if err := e.machine().Logout(e.io()); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "not paired") {
		t.Errorf("stdout = %q", e.stdout)
	}
	if reason := e.sandbox.PausedReason(); reason != "" {
		t.Errorf("an unpaired logout paused recording: %q", reason)
	}
}

func TestEnableOnAnUnpairedDevicePairsFirst(t *testing.T) {
	e := newUnpairedEnv(t)
	e.pairable()
	e.startProxy()

	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "not paired yet") {
		t.Errorf("stdout = %q, want the pairing step announced", e.stdout)
	}
	if !e.machine().Paired() {
		t.Error("device not paired after enable")
	}
	if !e.status().Enabled {
		t.Error("project not enabled after pairing")
	}
}

func TestPurgeSendsADeletionRequestForThisProjectOnly(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	grant := e.status()
	e.service.Stub("POST", "/v1/data-deletions", fakeplatform.JSON(202, map[string]any{}))

	if err := e.machine().Disable(e.project, true, e.io()); err != nil {
		t.Fatalf("disable --purge: %v", err)
	}
	reqs := e.service.Requests()
	last := reqs[len(reqs)-1]
	if last.URL != "/v1/data-deletions" {
		t.Fatalf("last request = %+v, want the deletion", last)
	}
	var body map[string]string
	if err := json.Unmarshal(last.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["project_id_hash"] != grant.GrantHash {
		t.Errorf("deletion hash = %q, want %q", body["project_id_hash"], grant.GrantHash)
	}
}

func TestPurgeOnANeverEnabledProjectStillRequestsDeletion(t *testing.T) {
	e := newEnv(t)
	e.service.Stub("POST", "/v1/data-deletions", fakeplatform.JSON(202, map[string]any{}))

	// The project was never enabled on this device, but the same root may
	// have contributed under an earlier enable; deletion is scoped by
	// project hash, not by the current grant, so the request still goes out.
	if err := e.machine().Disable(e.project, true, e.io()); err != nil {
		t.Fatalf("disable --purge: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "not enabled") {
		t.Errorf("stdout = %q, want the no-op local disable explained", e.stdout)
	}
	reqs := e.service.Requests()
	if len(reqs) == 0 {
		t.Fatal("no deletion request was sent")
	}
	last := reqs[len(reqs)-1]
	if last.URL != "/v1/data-deletions" {
		t.Fatalf("last request = %+v, want the deletion", last)
	}
	var body map[string]string
	if err := json.Unmarshal(last.Body, &body); err != nil {
		t.Fatal(err)
	}
	if want := consent.ProjectIDHash(e.canonicalRoot()); body["project_id_hash"] != want {
		t.Errorf("deletion hash = %q, want %q", body["project_id_hash"], want)
	}
}

func TestPurgeWithoutAPairedDeviceStillDisablesLocally(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	if err := e.deps.Tokens.ClearDeviceToken(); err != nil {
		t.Fatal(err)
	}

	err := e.machine().Disable(e.project, true, e.io())
	if err == nil || !strings.Contains(err.Error(), "trajector login") {
		t.Errorf("disable --purge = %v, want the missing pairing explained", err)
	}
	if e.status().Enabled {
		t.Error("the local disable did not happen")
	}
}

func TestUninstallRemovesEveryInjectionAndKeepsDataByDefault(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Login(e.io()); err != nil {
		t.Fatal(err)
	}
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	e.stdin = "\n"
	if err := e.machine().Uninstall(false, e.io()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if e.status().InjectedBaseURL != "" {
		t.Error("project injection survived uninstall")
	}
	if claudesettings.HasHook(claudesettings.UserSettingsPath(e.deps.Home), claudesettings.DiscoveryMarker) {
		t.Error("discovery hint survived uninstall")
	}
	if !e.machine().Paired() {
		t.Error("device token removed despite keeping data")
	}
	if _, err := os.Stat(e.layout().RoutingTable()); err != nil {
		t.Errorf("configuration removed despite keeping data: %v", err)
	}
}

func TestUninstallPointsAtLeftoverIgnoreLinesWithoutEditingThem(t *testing.T) {
	e := newEnv(t)
	e.gitRepo()
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(e.canonicalRoot(), ".gitignore")
	before, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatal(err)
	}

	e.stdin = "\n"
	e.stdout.Reset()
	if err := e.machine().Uninstall(false, e.io()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	out := e.stdout.String()
	for _, want := range []string{
		ignorePath,
		".claude/settings.local.json",
		"trajector-doctor-*.tar.gz",
		"trajector-doctor-*/",
		"remove those lines yourself",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to mention %q", out, want)
		}
	}
	after, err := os.ReadFile(ignorePath)
	if err != nil || string(after) != string(before) {
		t.Errorf(".gitignore changed across uninstall:\nbefore: %q\nafter: %q (%v)", before, after, err)
	}
}

func TestUninstallSkipsIgnoreNoteWithoutAnIgnoreFile(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	e.stdin = "\n"
	e.stdout.Reset()
	if err := e.machine().Uninstall(false, e.io()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if strings.Contains(e.stdout.String(), ".gitignore") {
		t.Errorf("stdout = %q, want no ignore note for a project without a .gitignore", e.stdout)
	}
}

func TestUninstallDeletesEverythingWhenAsked(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}

	if err := e.machine().Uninstall(true, e.io()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, dir := range e.layout().Roots() {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s still exists after confirmed cleanup", dir)
		}
	}
	if e.machine().Paired() {
		t.Error("device token survived confirmed cleanup")
	}
}
