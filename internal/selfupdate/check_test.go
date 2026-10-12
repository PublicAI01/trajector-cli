package selfupdate_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// checkedAt is the moment the first background check in a test runs.
var checkedAt = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// noFinish is the finish of a build whose own install is not left
// unfinished: a check that ran it would have to report a call that
// never should have happened.
func noFinish(t *testing.T) func(context.Context) error {
	return func(context.Context) error {
		t.Error("the check finished an install nothing named unfinished")
		return nil
	}
}

func TestState_DueOnceADayOrOnceAnHourWhileTheServiceWantsANewerVersion(t *testing.T) {
	for name, c := range map[string]struct {
		last   time.Time
		since  time.Duration
		behind bool
		due    bool
	}{
		"never checked":                       {due: true},
		"checked 23 hours ago":                {last: checkedAt, since: 23 * time.Hour},
		"checked 24 hours ago":                {last: checkedAt, since: 24 * time.Hour, due: true},
		"refused, checked 59 minutes ago":     {last: checkedAt, since: 59 * time.Minute, behind: true},
		"refused, checked an hour ago":        {last: checkedAt, since: time.Hour, behind: true, due: true},
		"last check later than the clock now": {last: checkedAt, since: -time.Hour, due: true},
	} {
		t.Run(name, func(t *testing.T) {
			w := selfupdate.Window{Now: checkedAt.Add(c.since), Behind: c.behind}
			if got := (selfupdate.State{CheckedAt: c.last}).Due(w); got != c.due {
				t.Errorf("Due = %v, want %v", got, c.due)
			}
		})
	}
}

func TestCheck_OnTheNewestReleaseRecordsWhenItLooked(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))

	c, err := selfupdate.Check(t.Context(), m.at("0.1.0"), releases.IndexURL(), selfupdate.Window{Now: checkedAt}, noFinish(t))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if c != (selfupdate.Checked{Result: selfupdate.ResultNewest}) {
		t.Errorf("Check = %+v, want the newest release found", c)
	}
	s := loadState(t, m.at("0.1.0"))
	if !s.CheckedAt.Equal(checkedAt) || s.Result != selfupdate.ResultNewest || s.Error != "" {
		t.Errorf("state is %+v, want the check recorded as newest at %s", s, checkedAt)
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("the binary is %q", got)
	}
	if got := releases.Downloads(); len(got) != 0 {
		t.Errorf("the check downloaded %v with nothing to install", got)
	}
}

func TestCheck_NotDueOnceTheLockIsTakenAsksNothing(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	in := m.at("0.1.0")
	writeState(t, in, `{"checked_at":"2026-10-12T09:00:00Z","result":"newest"}`)

	// Another check finished an hour ago: this one was started on a
	// state file read before that one wrote it.
	c, err := selfupdate.Check(t.Context(), in, "http://releases.invalid/releases", selfupdate.Window{Now: checkedAt.Add(time.Hour)}, noFinish(t))
	if err != nil || c != (selfupdate.Checked{}) {
		t.Errorf("Check = %+v, %v; want nothing done", c, err)
	}
	if s := loadState(t, in); !s.CheckedAt.Equal(checkedAt) || s.Result != selfupdate.ResultNewest {
		t.Errorf("state is %+v, want it untouched", s)
	}
}

func TestCheck_WhileAnotherUpgradeRunsChangesNothing(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	in := m.at("0.1.0")
	holdLock(t, in)

	_, err := selfupdate.Check(t.Context(), in, "http://releases.invalid/releases", selfupdate.Window{Now: checkedAt}, noFinish(t))
	if !errors.Is(err, selfupdate.ErrBusy) {
		t.Fatalf("Check = %v, want ErrBusy without waiting", err)
	}
	if s := loadState(t, in); !s.CheckedAt.IsZero() {
		t.Errorf("state is %+v, want nothing recorded", s)
	}
}

func TestCheck_ManagedInstallationIsToldOncePerRelease(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	in := m.at("0.1.0")
	in.ExecPath = filepath.Join(t.TempDir(), "Cellar", "trajector", "0.1.0", "bin", "trajector")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))

	check := func(at time.Time) selfupdate.Checked {
		t.Helper()
		c, err := selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: at}, noFinish(t))
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		return c
	}

	want := selfupdate.Checked{Result: selfupdate.ResultManaged, Release: "0.2.0", Manager: selfupdate.Homebrew, TellManaged: true}
	if c := check(checkedAt); c != want {
		t.Errorf("first check = %+v, want %+v", c, want)
	}
	if s := loadState(t, in); s.ToldManaged != "0.2.0" || s.Result != selfupdate.ResultManaged {
		t.Errorf("state is %+v, want 0.2.0 recorded as told", s)
	}
	// The same release a day later is not news.
	want.TellManaged = false
	if c := check(checkedAt.Add(24 * time.Hour)); c != want {
		t.Errorf("second check = %+v, want %+v", c, want)
	}
	releases.Publish(t, "0.3.0", []byte("the 0.3.0 binary"))
	want.Release, want.TellManaged = "0.3.0", true
	if c := check(checkedAt.Add(48 * time.Hour)); c != want {
		t.Errorf("check after a newer release = %+v, want %+v", c, want)
	}
	if got := releases.Downloads(); len(got) != 0 {
		t.Errorf("the check downloaded %v for an installation a package manager owns", got)
	}
}

func TestCheck_InstallsANewerReleaseAndRecordsIt(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	release := fakereleases.BuildRelease(t, "0.2.0")
	releases.Publish(t, "0.2.0", release)

	c, err := selfupdate.Check(t.Context(), m.at("0.1.0"), releases.IndexURL(), selfupdate.Window{Now: checkedAt}, noFinish(t))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if c != (selfupdate.Checked{Result: selfupdate.ResultUpgraded, Release: "0.2.0"}) {
		t.Errorf("Check = %+v, want 0.2.0 installed", c)
	}
	if !bytes.Equal(m.bytesOf(), release) {
		t.Error("the installed binary is not the 0.2.0 release")
	}
	s := loadState(t, m.at("0.1.0"))
	if !s.CheckedAt.Equal(checkedAt) || s.Result != selfupdate.ResultUpgraded || s.Installed != "0.2.0" || s.FinishPending != "" {
		t.Errorf("state is %+v, want 0.2.0 installed and finished at %s", s, checkedAt)
	}
}

func TestCheck_FailureIsRecordedAndTriedAgainInTheNextWindow(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	in := m.at("0.1.0")
	releases := fakereleases.New(t)
	index := releases.IndexURL() + "-elsewhere?token=secret"

	c, err := selfupdate.Check(t.Context(), in, index, selfupdate.Window{Now: checkedAt}, noFinish(t))
	if err == nil || c.Result != selfupdate.ResultFailed {
		t.Fatalf("Check = %+v, %v; want a failure", c, err)
	}
	s := loadState(t, in)
	if s.Result != selfupdate.ResultFailed || !s.CheckedAt.Equal(checkedAt) || s.Error == "" || strings.Contains(s.Error, "secret") {
		t.Errorf("state is %+v, want the failure on one line without the query", s)
	}

	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
	if c, err := selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt.Add(23 * time.Hour)}, noFinish(t)); err != nil || c.Result != "" {
		t.Errorf("Check inside the window = %+v, %v; want it not due", c, err)
	}
	if c, err := selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt.Add(24 * time.Hour)}, noFinish(t)); err != nil || c.Result != selfupdate.ResultNewest {
		t.Errorf("Check in the next window = %+v, %v; want it tried again", c, err)
	}
	if s := loadState(t, in); s.Error != "" {
		t.Errorf("recorded error is %q after a check that did not fail", s.Error)
	}
}

func TestCheck_OfABuildThatIsNotAPublishedReleaseFails(t *testing.T) {
	m := install(t, "the dev binary")
	c, err := selfupdate.Check(t.Context(), m.at("dev"), "http://releases.invalid/releases", selfupdate.Window{Now: checkedAt}, noFinish(t))
	if err == nil || c.Result != selfupdate.ResultFailed {
		t.Errorf("Check = %+v, %v; want a failure", c, err)
	}
}

func TestCheck_AReleaseThatDoesNotStartIsTriedOnceUntilANewerOne(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	in := m.at("0.1.0")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("these bytes are not a program"))

	c, err := selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt}, noFinish(t))
	unusable, ok := errors.AsType[*selfupdate.UnusableError](err)
	if !ok || unusable.Version != "0.2.0" || c.Result != selfupdate.ResultFailed || c.Release != "0.2.0" {
		t.Fatalf("Check = %+v, %v; want 0.2.0 found not to start here", c, err)
	}
	downloads := len(releases.Downloads())

	// A day later the same release is not fetched again, and the error
	// is not the one that says it was just tried: the user is told once.
	c, err = selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt.Add(24 * time.Hour)}, noFinish(t))
	if err == nil || c.Result != selfupdate.ResultFailed || !strings.Contains(err.Error(), unusable.Reason) {
		t.Errorf("Check = %+v, %v; want the recorded reason", c, err)
	}
	if _, again := errors.AsType[*selfupdate.UnusableError](err); again {
		t.Error("the release was reported as just tried again")
	}
	if got := len(releases.Downloads()); got != downloads {
		t.Errorf("the check fetched the release again: %v", releases.Downloads())
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("the binary is %q", got)
	}

	release := fakereleases.BuildRelease(t, "0.3.0")
	releases.Publish(t, "0.3.0", release)
	c, err = selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt.Add(48 * time.Hour)}, noFinish(t))
	if err != nil || c.Result != selfupdate.ResultUpgraded || c.Release != "0.3.0" {
		t.Fatalf("Check = %+v, %v; want the newer release installed", c, err)
	}
	if s := loadState(t, in); len(s.Unusable) != 0 {
		t.Errorf("unusable versions are %v, want the passed one dropped", s.Unusable)
	}
}

func TestCheck_FinishesThisBuildsUnfinishedInstallFirst(t *testing.T) {
	for name, c := range map[string]struct {
		pending  string
		finish   error
		calls    int
		left     string
		reported bool
	}{
		"this build's, finished":         {pending: "0.1.0", calls: 1},
		"this build's, failing again":    {pending: "0.1.0", finish: errors.New("cannot"), calls: 1, left: "0.1.0", reported: true},
		"another build's is not its own": {pending: "0.0.9", left: "0.0.9"},
	} {
		t.Run(name, func(t *testing.T) {
			m := install(t, "the 0.1.0 binary")
			in := m.at("0.1.0")
			writeState(t, in, `{"installed":"0.1.0","finish_pending":"`+c.pending+`"}`)
			releases := fakereleases.New(t)
			releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
			calls := 0
			finish := func(context.Context) error {
				calls++
				return c.finish
			}

			got, err := selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt}, finish)
			if err != nil || got.Result != selfupdate.ResultNewest || got.FinishFailed != c.reported {
				t.Errorf("Check = %+v, %v; want the newest release found, finish failing %v", got, err, c.reported)
			}
			if calls != c.calls {
				t.Errorf("finish ran %d times, want %d", calls, c.calls)
			}
			if s := loadState(t, in); s.FinishPending != c.left {
				t.Errorf("unfinished install is %q, want %q", s.FinishPending, c.left)
			}
		})
	}
}

func TestCheck_ReplacesAStateFileItCannotRead(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	in := m.at("0.1.0")
	writeState(t, in, "{not json")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))

	if c, err := selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt}, noFinish(t)); err != nil || c.Result != selfupdate.ResultNewest {
		t.Errorf("Check = %+v, %v; want it run", c, err)
	}
	if s := loadState(t, in); s.Result != selfupdate.ResultNewest {
		t.Errorf("state is %+v, want the check recorded", s)
	}
}
