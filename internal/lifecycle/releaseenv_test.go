package lifecycle_test

import (
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// A repository's committed settings reach a session hook's environment.
// The variables a test suite uses to keep the CLI from starting
// processes and to pin its clock must not let such settings stop a
// release build from checking for upgrades, or make every check it
// records look as old as the pinned instant.
func TestAReleaseBuildsSessionHookIgnoresTheTestOnlyVariables(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	installed := fakereleases.BuildRelease(t, "0.1.0")
	e.installBinary(string(installed))
	releases.Publish(t, "0.1.0", installed)
	e.sandbox.WriteUserConfig(`{"platform_url": "` + e.service.URL() + `", "releases_url": "` + releases.IndexURL() + `"}`)
	e.sandbox.AcceptAgreement(proxytest.AgreementVersion, "2026-08-01T00:00:00Z")
	e.startProxy()
	t.Setenv(cli.ProxyAddrEnv, e.deps.ProxyAddr)
	pinned := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Setenv(cli.RecordProxyStartEnv, "1")
	t.Setenv(cli.NowEnv, pinned.Format(time.RFC3339))

	before := time.Now()
	stdout, stderr, exit := e.run("hook", "ensure-proxy")
	if exit != 0 || stdout != "" || stderr != "" {
		t.Fatalf("hook ensure-proxy = exit %d, stdout %q, stderr %q; want a silent success", exit, stdout, stderr)
	}

	s := e.awaitBackgroundCheck(selfupdate.ResultNewest, 2*time.Minute)
	if s.CheckedAt.Equal(pinned) || s.CheckedAt.Before(before.Truncate(time.Second)) || s.CheckedAt.After(time.Now()) {
		t.Errorf("checked_at = %s, want the real time of the check, not the pinned %s", s.CheckedAt, pinned)
	}
}
