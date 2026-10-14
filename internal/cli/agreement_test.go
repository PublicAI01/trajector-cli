package cli_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// deviceSection returns the lines of the Device section in out, up to
// the blank line that ends it.
func deviceSection(t *testing.T, out string) string {
	t.Helper()
	_, rest, ok := strings.Cut(out, "\nDevice\n")
	if !ok {
		t.Fatalf("output has no Device section:\n%s", out)
	}
	body, _, _ := strings.Cut(rest, "\n\n")
	return body
}

func TestStatusAndDoctorReportAChangedAgreementBeforeAnySessionHookRuns(t *testing.T) {
	proxytest.RequireSessionSources(t)
	e := clitest.New(t)
	e.Paired()
	p := e.StartProxy()
	defer p.Stop()
	if got := e.InProjectInput("yes\n", "enable", "--no-earlier"); got.Exit != 0 {
		t.Fatalf("enable = exit %d\nstdout: %s\nstderr: %s", got.Exit, got.Stdout, got.Stderr)
	}
	if got := e.InProject("status"); !strings.HasPrefix(got.Stdout, "Recording: on") {
		t.Fatalf("status before the agreement changed:\n%s", got.Stdout)
	}
	// The first doctor run makes repairs of its own; the runs compared
	// below make none.
	e.InProject("doctor", "--all")
	// A build whose agreement changed, installed by replacing the binary:
	// the consent record holds the earlier terms and no pause is stored.
	e.Sandbox().AcceptAgreement("2020-01-obsolete", "2020-01-01T00:00:00Z")

	status := e.InProject("status")
	doctor := e.InProject("doctor", "--all")

	if reason := e.Sandbox().PausedReason(); reason != "" {
		t.Errorf("pause = %q after status and doctor, want none stored", reason)
	}
	if status.Exit != 1 || strings.HasPrefix(status.Stdout, "Recording: on") {
		t.Errorf("status = exit %d, want 1 and recording not on:\n%s", status.Exit, status.Stdout)
	}
	if doctor.Exit != 1 || strings.Contains(doctor.Stdout, "Everything checks out.") {
		t.Errorf("doctor --all = exit %d, want 1 and a problem:\n%s", doctor.Exit, doctor.Stdout)
	}
	for name, out := range map[string]string{"status": status.Stdout, "doctor --all": doctor.Stdout} {
		device := deviceSection(t, out)
		for _, want := range []string{
			routing.PauseConsentReconfirm.Why(),
			"fix:  trajector enable\n",
			routing.NoProxyCaveat,
		} {
			if !strings.Contains(device, want) {
				t.Errorf("%s device section lacks %q:\n%s", name, want, device)
			}
		}
	}

	e.InProject("hook", "ensure-proxy")
	if reason := e.Sandbox().PausedReason(); reason != routing.PauseConsentReconfirm {
		t.Fatalf("pause = %q after the session hook, want the reconfirmation pause", reason)
	}
	stored := e.InProject("status")
	storedDoctor := e.InProject("doctor", "--all")
	if verdict, _, _ := strings.Cut(stored.Stdout, "\n"); !strings.HasPrefix(status.Stdout, verdict+"\n") {
		t.Errorf("status verdict = %q with the pause stored, want the one before the hook:\n%s", verdict, status.Stdout)
	}
	if got, want := deviceSection(t, stored.Stdout), deviceSection(t, status.Stdout); got != want {
		t.Errorf("status device section with the pause stored:\n%s\nwant the one before the hook:\n%s", got, want)
	}
	if got, want := deviceSection(t, storedDoctor.Stdout), deviceSection(t, doctor.Stdout); got != want {
		t.Errorf("doctor device section with the pause stored:\n%s\nwant the one before the hook:\n%s", got, want)
	}
}
