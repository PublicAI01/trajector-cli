package lifecycle_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/cli"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// upgradeCheckArgv is the command line a session hook starts the
// background check with. It is spelled out rather than built from the
// constants that name it: a build that is about to be replaced starts
// it on whatever build is installed next.
var upgradeCheckArgv = []string{"hook", "upgrade-check"}

// starts is every command line this device's machine asked to start a
// process with, in order; nothing is started.
type starts struct {
	mu    sync.Mutex
	argvs [][]string
}

// recordStarts has every process this device would start recorded
// rather than started.
func (e *env) recordStarts() *starts {
	s := &starts{}
	e.deps.Spawn = func(_ context.Context, _ string, args []string, _ string) (int, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.argvs = append(s.argvs, slices.Clone(args))
		return 0, nil
	}
	return s
}

// upgradeChecks is the starts of the background check among them.
func (s *starts) upgradeChecks() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var checks [][]string
	for _, argv := range s.argvs {
		if len(argv) > 1 && argv[1] == "upgrade-check" {
			checks = append(checks, argv)
		}
	}
	return checks
}

// newCheckingEnv is a device every gate on the background check lets
// through: a release build of 0.1.0 whose user config leaves automatic
// upgrades on, whose accepted agreement tells the user about them, and
// that has never checked. Its proxy is up, so the session's hook starts
// nothing else.
func newCheckingEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.deps.Version = "0.1.0"
	e.deps.ReleaseBuild = true
	e.deps.AutoUpgrade = true
	e.sandbox.AcceptAgreement(proxytest.AgreementVersion, "2026-08-01T00:00:00Z")
	e.startProxy()
	return e
}

// lastChecked records a background check that ran ago before this
// device's clock.
func (e *env) lastChecked(ago time.Duration) {
	e.t.Helper()
	at := e.deps.Now().Add(-ago).UTC().Format(time.RFC3339)
	e.writeUpgradeState(`{"checked_at": "` + at + `", "result": "newest"}`)
}

func TestSessionStarting_StartsTheUpgradeCheckOnlyWhenEveryGatePasses(t *testing.T) {
	cases := map[string]struct {
		arrange func(e *env)
		starts  bool
	}{
		"never checked": {
			arrange: func(*env) {},
			starts:  true,
		},
		"checked a day ago": {
			arrange: func(e *env) { e.lastChecked(24 * time.Hour) },
			starts:  true,
		},
		"checked 23 hours ago": {
			arrange: func(e *env) { e.lastChecked(23 * time.Hour) },
		},
		"refused by the service, checked an hour ago": {
			arrange: func(e *env) {
				e.sandbox.SeedUpgradeRefusal("0.2.0", "")
				e.lastChecked(time.Hour)
			},
			starts: true,
		},
		"refused by the service, checked 59 minutes ago": {
			arrange: func(e *env) {
				e.sandbox.SeedUpgradeRefusal("0.2.0", "")
				e.lastChecked(59 * time.Minute)
			},
		},
		"a service minimum this version meets, checked an hour ago": {
			arrange: func(e *env) {
				e.sandbox.SeedUpgradeRefusal("0.1.0", "")
				e.lastChecked(time.Hour)
			},
		},
		"a state file that cannot be read": {
			arrange: func(e *env) { e.writeUpgradeState("{cut") },
			starts:  true,
		},
		"auto_upgrade false": {
			arrange: func(e *env) { e.deps.AutoUpgrade = false },
		},
		"a build the release pipeline did not make": {
			// Built from source with a version stamped or recorded by the
			// toolchain, which a release order can place.
			arrange: func(e *env) { e.deps.ReleaseBuild = false },
		},
		"an agreement accepted before the check was in it": {
			// The day before the Updates section first went out.
			arrange: func(e *env) { e.sandbox.AcceptAgreement(dayBefore(e.t, consent.UpdatesSince), "2026-08-01T00:00:00Z") },
		},
		"an accepted agreement that is not a release day": {
			arrange: func(e *env) { e.sandbox.AcceptAgreement("2020-01-obsolete", "2020-01-01T00:00:00Z") },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newCheckingEnv(t)
			tc.arrange(e)
			started := e.recordStarts()

			if _, err := e.machine().SessionStarting(t.Context(), e.project, lifecycle.HookInput{}, e.io()); err != nil {
				t.Fatalf("SessionStarting: %v", err)
			}

			var want [][]string
			if tc.starts {
				want = [][]string{upgradeCheckArgv}
			}
			if got := started.upgradeChecks(); !slices.EqualFunc(got, want, slices.Equal) {
				t.Errorf("upgrade checks started = %q, want %q", got, want)
			}
			// Whatever the hook starts, what it prints reaches the model.
			if e.stdout.Len() != 0 {
				t.Errorf("the hook printed %q", e.stdout)
			}
		})
	}
}

// dayBefore is the release day before day.
func dayBefore(t *testing.T, day string) string {
	t.Helper()
	d, err := time.Parse(time.DateOnly, day)
	if err != nil {
		t.Fatal(err)
	}
	return d.AddDate(0, 0, -1).Format(time.DateOnly)
}

func TestSessionStarting_StartsTheUpgradeCheckOnADeviceThatSignedOut(t *testing.T) {
	e := newCheckingEnv(t)
	if err := e.machine().Logout(t.Context(), discardIO()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	started := e.recordStarts()

	// The agreement it accepted still stands, and its sessions still
	// go through trajector.
	if _, err := e.machine().SessionStarting(t.Context(), e.project, lifecycle.HookInput{}, e.io()); err != nil {
		t.Fatalf("SessionStarting: %v", err)
	}
	if got := started.upgradeChecks(); !slices.EqualFunc(got, [][]string{upgradeCheckArgv}, slices.Equal) {
		t.Errorf("upgrade checks started = %q, want one", got)
	}
}

func TestDetachUpgradeCheck_StartsTheCheckMarkedAsTheOneThatRuns(t *testing.T) {
	e := newEnv(t)
	started := e.recordStarts()

	if err := e.machine().DetachUpgradeCheck(t.Context()); err != nil {
		t.Fatalf("DetachUpgradeCheck: %v", err)
	}
	want := [][]string{{"hook", "upgrade-check", "--detached"}}
	if got := started.upgradeChecks(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("started %q, want %q", got, want)
	}
}

func TestDetachUpgradeCheck_ReportsACheckThatCouldNotStart(t *testing.T) {
	e := newEnv(t)
	e.deps.Spawn = func(context.Context, string, []string, string) (int, error) {
		return 0, errors.New("exec format error")
	}
	if err := e.machine().DetachUpgradeCheck(t.Context()); err == nil {
		t.Error("DetachUpgradeCheck reported a start that failed as done")
	}
}

func TestCheckForUpgrade_TellsAHomebrewUserOncePerRelease(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	e.deps.ExecPath = exeName(filepath.Join(e.deps.Home, "Cellar", "trajector", "0.1.0", "bin", "trajector"))
	e.installBinary("the binary Homebrew installed")
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))

	e.machine().CheckForUpgrade(t.Context())
	want := []string{"trajector 0.2.0 is available. Run `brew upgrade trajector` to install it."}
	if got := e.notices(); !slices.Equal(got, want) {
		t.Errorf("notices = %q, want %q", got, want)
	}

	e.deps.Now = func() time.Time { return time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC) }
	e.machine().CheckForUpgrade(t.Context())
	if got := e.notices(); len(got) != 0 {
		t.Errorf("notices after the same release a day later = %q, want none", got)
	}
	if got := e.installedBinary(); got != "the binary Homebrew installed" {
		t.Errorf("the binary is %q", got)
	}
}

func TestCheckForUpgrade_TellsOnceOfAReleaseThatDoesNotStart(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.2.0", []byte("these bytes are not a program"))

	e.machine().CheckForUpgrade(t.Context())
	want := []string{"trajector 0.2.0 does not start on this machine, so 0.1.0 was kept. `trajector upgrade` tries again."}
	if got := e.notices(); !slices.Equal(got, want) {
		t.Errorf("notices = %q, want %q", got, want)
	}

	e.deps.Now = func() time.Time { return time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC) }
	e.machine().CheckForUpgrade(t.Context())
	if got := e.notices(); len(got) != 0 {
		t.Errorf("notices a day later = %q, want none", got)
	}
}

func TestCheckForUpgrade_FailsSilently(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Ration()

	e.machine().CheckForUpgrade(t.Context())
	if got := e.notices(); len(got) != 0 {
		t.Errorf("notices = %q, want none", got)
	}
	s, err := selfupdate.LoadState(e.layout().UpgradeState())
	if err != nil || s.Result != selfupdate.ResultFailed || s.Error == "" {
		t.Errorf("state = %+v, %v; want the failure recorded", s, err)
	}
}

func TestCheckForUpgrade_SendsTheUserToDoctorWhenItsSetupFailsTwice(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
	index := holdIndex(t, releases.IndexURL())
	e.deps.Releases = index.URL
	// The build that installed this one had it set itself up once, and
	// that failed.
	e.writeUpgradeState(`{"installed": "0.1.0", "finish_pending": "0.1.0", "finish_failures": 1}`)
	// The setup fails again: the lines it leaves for the user cannot be
	// written while a directory stands where they go. The check asks
	// the release index after the setup ran, and by the time it is
	// answered the obstruction is gone, so the check's own line lands.
	if err := os.MkdirAll(e.layout().Notices(), 0o700); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.machine().CheckForUpgrade(context.WithoutCancel(t.Context()))
	}()
	select {
	case <-index.asked:
	case <-time.After(30 * time.Second):
		t.Fatal("the check never asked for the release index")
	}
	if err := os.Remove(e.layout().Notices()); err != nil {
		t.Fatal(err)
	}
	index.letGo()
	<-done

	want := []string{"The upgrade to 0.1.0 is installed but its setup did not finish. Run `trajector doctor --all`."}
	if got := e.notices(); !slices.Equal(got, want) {
		t.Errorf("notices = %q, want %q", got, want)
	}
	if s, _ := selfupdate.LoadState(e.layout().UpgradeState()); s.FinishPending != "" || s.FinishFailures != 0 {
		t.Errorf("state = %+v, want the setup no longer retried", s)
	}
}

// blockNotices stands a directory where this device's notice store
// keeps its lines, so none can be left until unblock is called.
func (e *env) blockNotices() (unblock func()) {
	e.t.Helper()
	if err := os.MkdirAll(e.layout().Notices(), 0o700); err != nil {
		e.t.Fatal(err)
	}
	return func() {
		e.t.Helper()
		if err := os.Remove(e.layout().Notices()); err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestCheckForUpgrade_TellsAHomebrewUserOfAReleaseTheLinesCouldNotBeLeftForAgain(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	e.deps.ExecPath = exeName(filepath.Join(e.deps.Home, "Cellar", "trajector", "0.1.0", "bin", "trajector"))
	e.installBinary("the binary Homebrew installed")
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	unblock := e.blockNotices()

	e.machine().CheckForUpgrade(t.Context())
	if s, _ := selfupdate.LoadState(e.layout().UpgradeState()); s.ToldManaged != "" {
		t.Errorf("state = %+v, want 0.2.0 not recorded as told", s)
	}

	unblock()
	e.deps.Now = func() time.Time { return time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC) }
	e.machine().CheckForUpgrade(t.Context())
	want := []string{"trajector 0.2.0 is available. Run `brew upgrade trajector` to install it."}
	if got := e.notices(); !slices.Equal(got, want) {
		t.Errorf("notices a day later = %q, want %q", got, want)
	}
}

func TestCheckForUpgrade_KeepsTryingASetupItCouldNotSendTheUserToDoctorFor(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
	e.writeUpgradeState(`{"installed": "0.1.0", "finish_pending": "0.1.0", "finish_failures": 1}`)
	e.blockNotices()

	e.machine().CheckForUpgrade(t.Context())
	if s, _ := selfupdate.LoadState(e.layout().UpgradeState()); s.FinishPending != "0.1.0" {
		t.Errorf("state = %+v, want the setup still named unfinished", s)
	}
}

func TestCheckForUpgrade_EndsWithALineWhenItPutsBackTheBuildAnInterruptedUpgradeKept(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	// An upgrade from 0.0.9 to this build stopped before this build
	// accepted the install.
	if err := os.WriteFile(e.deps.ExecPath+".old-0.0.9"+filepath.Ext(e.deps.ExecPath), []byte("the 0.0.9 binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	e.machine().CheckForUpgrade(t.Context())
	want := []string{"An upgrade to trajector 0.1.0 was interrupted, so 0.0.9 was put back. `trajector upgrade` tries again."}
	if got := e.notices(); !slices.Equal(got, want) {
		t.Errorf("notices = %q, want %q", got, want)
	}
	if got := e.installedBinary(); got != "the 0.0.9 binary" {
		t.Errorf("installed binary is %q, want the one put back", got)
	}
	if got := releases.Downloads(); len(got) != 0 {
		t.Errorf("downloaded %v after putting a build back", got)
	}
}

func TestCheckForUpgrade_WhileAnotherUpgradeRunsDoesNothing(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	if err := os.MkdirAll(filepath.Dir(e.layout().UpgradeLock()), 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := fsatomic.Lock(e.layout().UpgradeLock(), time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	e.machine().CheckForUpgrade(t.Context())
	if s, _ := selfupdate.LoadState(e.layout().UpgradeState()); !s.CheckedAt.IsZero() {
		t.Errorf("state = %+v, want nothing recorded", s)
	}
	if got := releases.Downloads(); len(got) != 0 {
		t.Errorf("downloaded %v", got)
	}
}

// heldIndex stands between a device and its release index and holds
// the first request for the index until it is let go, so a test can
// look at the machine while the check is waiting on the network.
type heldIndex struct {
	URL     string
	asked   chan http.Header
	letGo   func()
	release chan struct{}
}

func holdIndex(t *testing.T, index string) *heldIndex {
	t.Helper()
	target, err := url.Parse(index)
	if err != nil {
		t.Fatal(err)
	}
	h := &heldIndex{asked: make(chan http.Header, 1), release: make(chan struct{})}
	h.letGo = sync.OnceFunc(func() { close(h.release) })
	forward := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: target.Scheme, Host: target.Host})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case h.asked <- r.Header.Clone():
		default:
		}
		<-h.release
		r.URL.Path = target.Path
		forward.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	// Runs before the server closes: a check still held would keep it
	// from closing.
	t.Cleanup(h.letGo)
	h.URL = srv.URL + "/releases"
	return h
}

// awaitBackgroundCheck waits until the state file records result and
// no upgrade holds the lock any longer, so whatever ran in the
// background is done.
func (e *env) awaitBackgroundCheck(result selfupdate.Result, within time.Duration) selfupdate.State {
	e.t.Helper()
	deadline := time.Now().Add(within)
	for {
		s, err := selfupdate.LoadState(e.layout().UpgradeState())
		if err == nil && s.Result == result {
			if unlock, err := fsatomic.Lock(e.layout().UpgradeLock(), time.Hour, 0); err == nil {
				unlock()
				return s
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("the background check did not come to %q in %s; state = %+v, %v", result, within, s, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// run runs the binary installed on this device with args, as a user or
// a session hook would, and returns what it printed.
func (e *env) run(args ...string) (stdout, stderr string, exit int) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.deps.ExecPath, args...)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	// A process that leaves a child holding these streams keeps the
	// command from returning, as it keeps a session's hook from ending.
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return out.String(), errs.String(), exitErr.ExitCode()
	} else if err != nil {
		e.t.Fatalf("running %s %q: %v (stderr %q)", e.deps.ExecPath, args, err, errs.String())
	}
	return out.String(), errs.String(), 0
}

func TestUpgradeCheck_EndToEnd_InstallsInTheBackgroundAndTellsTheNextCommand(t *testing.T) {
	e, releases := newUpgradeEnv(t)
	e.installBinary(string(fakereleases.BuildRelease(t, "0.1.0")))
	release := fakereleases.BuildRelease(t, "0.2.0")
	releases.Publish(t, "0.2.0", release)
	index := holdIndex(t, releases.IndexURL())
	// The installed build reads where it uploads to and where releases
	// are listed from the user config, as every trajector command does.
	e.sandbox.WriteUserConfig(`{"platform_url": "` + e.service.URL() + `", "releases_url": "` + index.URL + `"}`)
	t.Setenv(cli.ProxyAddrEnv, e.deps.ProxyAddr)

	// A session starts: its hook starts the check.
	e.deps.ReleaseBuild, e.deps.AutoUpgrade = true, true
	e.sandbox.AcceptAgreement(proxytest.AgreementVersion, "2026-08-01T00:00:00Z")
	e.startProxy()
	started := e.recordStarts()
	if _, err := e.machine().SessionStarting(t.Context(), e.project, lifecycle.HookInput{}, e.io()); err != nil {
		t.Fatalf("SessionStarting: %v", err)
	}
	checks := started.upgradeChecks()
	if len(checks) != 1 {
		t.Fatalf("upgrade checks started = %q, want one", checks)
	}

	// What the hook started returns at once, with nothing printed and
	// no stream left open, while the check it started is still waiting
	// for the release index.
	stdout, stderr, exit := e.run(checks[0]...)
	if exit != 0 || stdout != "" || stderr != "" {
		t.Fatalf("%q = exit %d, stdout %q, stderr %q; want a silent success", checks[0], exit, stdout, stderr)
	}
	var asked http.Header
	select {
	case asked = <-index.asked:
	case <-time.After(30 * time.Second):
		t.Fatal("no check asked for the release index")
	}
	if s, _ := selfupdate.LoadState(e.layout().UpgradeState()); s.Result != "" {
		t.Fatalf("state = %+v before the index answered; want the check still running", s)
	}
	if got := asked.Get("User-Agent"); got != "trajector/0.1.0" {
		t.Errorf("the index was asked as %q, want trajector/0.1.0", got)
	}
	for name := range asked {
		if name != "User-Agent" && name != "Accept-Encoding" {
			t.Errorf("the index was asked with a %s header", name)
		}
	}

	index.letGo()
	s := e.awaitBackgroundCheck(selfupdate.ResultUpgraded, 2*time.Minute)
	if s.Installed != "0.2.0" || s.FinishPending != "" {
		t.Errorf("state = %+v, want 0.2.0 installed and finished", s)
	}
	if !bytes.Equal([]byte(e.installedBinary()), release) {
		t.Error("the installed binary is not the 0.2.0 release")
	}

	// The user's next command hears of it, once.
	_, stderr, _ = e.run("status")
	if !strings.Contains(stderr, "trajector upgraded itself to 0.2.0 in the background.") {
		t.Errorf("status stderr = %q, want the upgrade told", stderr)
	}
	if _, stderr, _ = e.run("status"); strings.Contains(stderr, "upgraded itself") {
		t.Errorf("the second status stderr = %q, want it told once", stderr)
	}
}

// The command line opens no machine on a user config it cannot read, so
// a session hook of a release build refuses before any gate on the
// background check is looked at, and starts nothing.
func TestAReleaseBuildsSessionHookOnAConfigFileItCannotReadStartsNoCheck(t *testing.T) {
	e, _ := newUpgradeEnv(t)
	e.installBinary(string(fakereleases.BuildRelease(t, "0.1.0")))
	e.sandbox.WriteUserConfig("{cut")
	e.sandbox.AcceptAgreement(proxytest.AgreementVersion, "2026-08-01T00:00:00Z")
	e.startProxy()
	t.Setenv(cli.ProxyAddrEnv, e.deps.ProxyAddr)

	stdout, stderr, exit := e.run("hook", "ensure-proxy")
	if exit != 1 || stdout != "" || !strings.Contains(stderr, "config.json") {
		t.Errorf("hook ensure-proxy = exit %d, stdout %q, stderr %q; want a refusal naming config.json", exit, stdout, stderr)
	}
	if _, err := os.Stat(e.layout().UpgradeState()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("upgrade state file: %v, want none written", err)
	}
}
