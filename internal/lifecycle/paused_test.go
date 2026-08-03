package lifecycle_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/tokenstore"
)

func TestOpenRequiresItsCollaborators(t *testing.T) {
	e := newEnv(t)
	base := e.deps

	withoutTokens := base
	withoutTokens.Tokens = nil
	if _, err := lifecycle.Open(withoutTokens); err == nil {
		t.Error("Open succeeded without a token store")
	}

	withoutService := base
	withoutService.Platform = nil
	if _, err := lifecycle.Open(withoutService); err == nil {
		t.Error("Open succeeded without a service client")
	}

	minimal := lifecycle.Deps{
		Layout:   base.Layout,
		Tokens:   tokenstore.Files(base.Layout.SecretsDir()),
		Platform: platform.New("http://127.0.0.1:1", "testv"),
	}
	if _, err := lifecycle.Open(minimal); err != nil {
		t.Errorf("Open with only the required collaborators = %v", err)
	}
}

// Each pause reason must reach the user as something they can act on,
// not as "this project would not be recorded".
func TestEnableExplainsWhyRecordingIsPaused(t *testing.T) {
	tests := []struct {
		name, reason, want string
	}{
		{"signed out", proxytest.PausedSignedOut, "trajector login"},
		{"agreement needs reconfirming", proxytest.PausedConsentReconfirm, "data agreement changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.startProxy()
			// Accepting the current agreement first keeps enable from
			// lifting the pause on its way through.
			if err := e.consentStore().AcceptAgreement(consent.AgreementVersion, "2026-08-01T00:00:00Z"); err != nil {
				t.Fatal(err)
			}
			e.sandbox.Pause(tt.reason)

			err := e.machine().Enable(e.project, e.io())
			if err == nil {
				t.Fatalf("enable succeeded while recording was paused for %q", tt.reason)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestUninstallOnACleanMachineReportsNothingToRemove(t *testing.T) {
	e := newEnv(t)
	if err := e.machine().Uninstall(false, e.io()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "0 project(s)") {
		t.Errorf("stdout = %q, want nothing removed", e.stdout)
	}
	if !strings.Contains(e.stdout.String(), "Local data kept") {
		t.Errorf("stdout = %q", e.stdout)
	}
}

func TestDisableOnANeverEnabledProjectDeletesNothing(t *testing.T) {
	e := newEnv(t)
	if err := e.machine().Disable(e.project, false, e.io()); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "not enabled") {
		t.Errorf("stdout = %q", e.stdout)
	}
	if len(e.sandbox.Rawcalls()) != 0 {
		t.Error("a spool that was never written to now has records")
	}
}

func TestSessionHooksSurviveAProjectDirectoryThatIsGone(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	missing := e.project + "-gone"

	// A hook runs inside whatever directory the session is in; it must
	// not break a session because that directory moved.
	e.machine().Discovery(missing, e.io())
	if err := e.machine().EnsureProxy(missing, e.io()); err != nil {
		t.Errorf("ensure-proxy on a missing project = %v, want the proxy up anyway", err)
	}
}

func TestPurgeReportsAFailedDeletionRequestWithoutUndoingTheLocalDisable(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	// No stub for the deletion endpoint, so the service refuses it.
	err := e.machine().Disable(e.project, true, e.io())
	if err == nil || !strings.Contains(err.Error(), "disabled locally") {
		t.Errorf("disable --purge = %v, want the local disable confirmed and the request reported", err)
	}
	if e.status().Enabled {
		t.Error("the local disable was undone by the failed request")
	}
}

func TestEnableRefusesAnUnreachableService(t *testing.T) {
	e := newUnpairedEnv(t)
	// No pairing stubs, so the service refuses to start one.
	err := e.machine().Enable(e.project, e.io())
	if err == nil || !strings.Contains(err.Error(), "starting pairing") {
		t.Errorf("enable = %v, want the pairing failure", err)
	}
	if e.status().Enabled {
		t.Error("a project was enabled without a paired device")
	}
}

func TestLoginKeepsAnExistingDiscoveryHook(t *testing.T) {
	e := newEnv(t)
	if err := e.machine().Login(e.io()); err != nil {
		t.Fatal(err)
	}
	before := e.userSettingsContents()
	e.stdout.Reset()

	if err := e.machine().Login(e.io()); err != nil {
		t.Fatal(err)
	}
	if after := e.userSettingsContents(); after != before {
		t.Errorf("a second login rewrote the user settings:\n%s\n%s", before, after)
	}
}
