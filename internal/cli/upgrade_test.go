package cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/clitest"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
)

func TestUpgradeTakesNoArguments(t *testing.T) {
	e := clitest.New(t)

	got := e.Run("upgrade", "0.2.0")
	if got.Exit != 2 {
		t.Fatalf("exit = %d, want 2 (stdout: %q)", got.Exit, got.Stdout)
	}
	// Which release to move to is not the user's to name: the release
	// source decides, so an argument is a misunderstanding to correct
	// rather than a version to honor.
	if !strings.Contains(got.Stderr, "usage: trajector upgrade") {
		t.Errorf("stderr = %q, want the usage line", got.Stderr)
	}
}

func TestUpgradeOfABuildFromACheckoutInstallsNothing(t *testing.T) {
	e := clitest.New(t)
	releases := fakereleases.New(t)
	releases.Publish(t, "9.9.9", []byte("a much newer binary"))
	e.SetReleasesURL(releases.IndexURL())

	// A test binary announces the development version, which no
	// release order contains.
	got := e.Run("upgrade")
	if got.Exit != 0 {
		t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "not a published release") {
		t.Errorf("stdout = %q, want the development build explained", got.Stdout)
	}
	if downloads := releases.Downloads(); len(downloads) != 0 {
		t.Errorf("upgrade downloaded %v from a build it will not replace", downloads)
	}
}

func TestUpgradeFailsLoudlyOnAConfigFileItCannotRead(t *testing.T) {
	e := clitest.New(t)
	e.WriteConfig("{not json")

	// The release source is configured in the same file as the service
	// endpoint, and read the same way: a file this command cannot read
	// might be the one naming a source other than the default.
	got := e.Run("upgrade")
	if got.Exit != 1 || !strings.Contains(got.Stderr, "config.json") {
		t.Errorf("upgrade = exit %d, stderr %q, want the unreadable config named", got.Exit, got.Stderr)
	}
}

func TestUpgradeIsNamedWhereverThisBuildIsFoundTooOld(t *testing.T) {
	// The service can pause uploads over the client version. Every
	// surface that reports that has to name the one command that
	// resolves it, or the user is told they have a problem and not what
	// to do about it.
	const hint = "trajector upgrade"

	t.Run("upload", func(t *testing.T) {
		e := clitest.New(t)
		e.Paired()
		e.Service().Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{"min_client_version": "9.9.9"}))
		seedRawcall(e, "req-1", time.Now().UTC().Add(-25*time.Hour))
		p := e.StartProxy()
		defer p.Stop()

		got := e.Run("upload", "--force")
		if got.Exit != 0 {
			t.Fatalf("exit = %d (stderr: %q)", got.Exit, got.Stderr)
		}
		// The kept-data reassurance and the remedy are separate lines:
		// a user who reads only the first still learns nothing was lost.
		if !strings.Contains(got.Stdout, "Captured data is kept.\n") {
			t.Errorf("stdout = %q, want the kept-data line on its own", got.Stdout)
		}
		if !strings.Contains(got.Stdout, hint) {
			t.Errorf("stdout = %q, want it to name %q", got.Stdout, hint)
		}

		t.Run("status", func(t *testing.T) {
			// status reports what the service last said, which it
			// learned from the upload above.
			got := e.InProject("status")
			if !strings.Contains(got.Stdout, "requires client version 9.9.9") {
				t.Fatalf("stdout = %q, want the service's requirement", got.Stdout)
			}
			if !strings.Contains(got.Stdout, hint) {
				t.Errorf("stdout = %q, want it to name %q", got.Stdout, hint)
			}
		})

		t.Run("doctor", func(t *testing.T) {
			got := e.InProject("doctor")
			if !strings.Contains(got.Stdout, "requires client version 9.9.9") {
				t.Fatalf("stdout = %q, want the service's requirement", got.Stdout)
			}
			if !strings.Contains(got.Stdout, hint) {
				t.Errorf("stdout = %q, want it to name %q", got.Stdout, hint)
			}
		})
	})
}

func TestWhatTheServiceSaysAboutTheVersionReachesTheUser(t *testing.T) {
	// A required version says the client is behind; only the service can
	// say why and by when. That sentence is written by the service, held
	// by the proxy, and printed by three other processes — so it is
	// tested where it has actually crossed all of them, and it must not
	// be able to write a line of its own on the way.
	const said = "Upload format 0.1.x is retired on 2026-09-01."
	e := clitest.New(t)
	e.Paired()
	e.Service().Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{
		"min_client_version": "9.9.9",
		"message":            said + "\rtrajector: everything is fine",
	}))
	seedRawcall(e, "req-1", time.Now().UTC().Add(-25*time.Hour))
	p := e.StartProxy()
	defer p.Stop()

	if got := e.Run("upload", "--force"); got.Exit != 0 {
		t.Fatalf("upload exit = %d (stderr: %q)", got.Exit, got.Stderr)
	}
	for _, surface := range []string{"upload", "status", "doctor"} {
		t.Run(surface, func(t *testing.T) {
			var out string
			if surface == "upload" {
				out = e.Run("upload").Stdout
			} else {
				out = e.InProject(surface).Stdout
			}
			if !strings.Contains(out, said) {
				t.Errorf("%s stdout = %q, want the service's sentence", surface, out)
			}
			if strings.Contains(out, "\rtrajector") || strings.Contains(out, "\ntrajector: everything is fine") {
				t.Errorf("%s stdout = %q, want the forged line disarmed", surface, out)
			}
		})
	}
}
