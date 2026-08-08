package lifecycle_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

func TestDoctorReportsAnOlderProxyItCannotReplace(t *testing.T) {
	e := newEnv(t)
	e.deps.Version = "2.0.0"
	e.startProxy(proxytest.WithVersion("1.0.0"))
	// Replacing the strictly older proxy means draining it and spawning
	// this binary — and this environment's exec path does not exist, so
	// the takeover must fail loudly, not silently.
	problems, out := e.doctor()

	if err := e.proxyEnv.WaitStopped(5 * time.Second); err != nil {
		t.Errorf("Serve = %v after doctor's takeover, want the older proxy drained", err)
	}
	if problems == 0 {
		t.Fatalf("problems = 0 with an irreplaceable older proxy, output:\n%s", out)
	}
	if !strings.Contains(out, "could not be replaced") {
		t.Errorf("doctor = %q, want the failed takeover reported", out)
	}
}

func TestDoctorFlagsAnUnsupportedChannel(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	e.environ["CLAUDE_CODE_USE_BEDROCK"] = "1"

	e.stdout.Reset()
	problems, out := e.doctor()
	if problems == 0 {
		t.Fatalf("problems = 0 with a Bedrock channel active, output:\n%s", out)
	}
	if !strings.Contains(out, "CLAUDE_CODE_USE_BEDROCK") {
		t.Errorf("doctor = %q, want the channel variable named", out)
	}
}

func TestDoctorReportsAnIdentityDisagreement(t *testing.T) {
	e := newEnv(t)
	e.startProxy()
	if err := e.machine().Enable(e.project, e.io()); err != nil {
		t.Fatal(err)
	}
	// The routing table now claims this root under a different project
	// hash than the consent record derives: nothing safe can be rewritten
	// from either side alone.
	grant, ok := e.sandbox.ActiveGrant(e.canonicalRoot())
	if !ok {
		t.Fatal("no grant after enable")
	}
	grant.ProjectIDHash = "hash-of-somewhere-else"
	e.sandbox.GrantProject(grant)

	e.stdout.Reset()
	problems, out := e.doctor()
	if problems == 0 {
		t.Fatalf("problems = 0 with disagreeing identities, output:\n%s", out)
	}
	if !strings.Contains(out, "disagree about this project's identity") {
		t.Errorf("doctor = %q, want the identity disagreement reported", out)
	}
}
