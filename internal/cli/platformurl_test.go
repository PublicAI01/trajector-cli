package cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
)

func TestUploadIgnoresAPlatformURLEnvironmentVariable(t *testing.T) {
	e := newUploadEnv(t)
	t.Setenv("TRAJECTOR_PLATFORM_URL", "http://127.0.0.1:9")
	seedRawcall(e, "req-1", time.Now().UTC())
	p := e.StartProxy()
	defer p.Stop()

	got := e.Run("upload", "--force")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if n := batchUploads(e.Service()); n != 1 {
		t.Errorf("the configured service saw %d uploads, want 1 with the environment override ignored", n)
	}
}

func TestNonLoopbackHTTPEndpointRefusesUploadsButCaptureContinues(t *testing.T) {
	e := clitest.New(t)
	e.Paired()
	e.SetPlatformURL("http://203.0.113.7:1")
	seedRawcall(e, "req-1", time.Now().UTC())
	p := e.StartProxy()
	defer p.Stop()

	got := e.Run("upload", "--force")
	if got.Exit != 1 || !strings.Contains(got.Stderr, "https") {
		t.Errorf("upload = exit %d, stderr %q, want a refusal naming the https requirement", got.Exit, got.Stderr)
	}
	if n := len(e.Sandbox().Rawcalls()); n != 1 {
		t.Errorf("spool holds %d rawcalls, want the captured data kept", n)
	}

	enabled := e.InProjectInput("yes\n", "enable")
	if enabled.Exit != 0 {
		t.Errorf("enable = exit %d (stderr: %q), want capture unaffected by the refused endpoint", enabled.Exit, enabled.Stderr)
	}
}

func TestMalformedConfigFileFailsCommandsLoudly(t *testing.T) {
	e := clitest.New(t)
	e.WriteConfig("{not json")

	got := e.Run("status")
	if got.Exit != 1 || !strings.Contains(got.Stderr, "config.json") {
		t.Errorf("status = exit %d, stderr %q, want the unreadable config named", got.Exit, got.Stderr)
	}
}
