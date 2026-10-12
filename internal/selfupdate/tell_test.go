package selfupdate_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// teller is an Installation.Tell that hears what a check tells, answers
// with each error of answers in turn and then nil, and checks that the
// check still holds the upgrade lock while it tells.
type teller struct {
	t       *testing.T
	in      selfupdate.Installation
	answers []error
	heard   []selfupdate.Checked
}

func (tl *teller) tell(c selfupdate.Checked, _ error) error {
	tl.t.Helper()
	tl.heard = append(tl.heard, c)
	if _, err := selfupdate.Check(tl.t.Context(), tl.in, "http://releases.invalid/releases", selfupdate.Window{Now: checkedAt}, noFinish(tl.t)); !errors.Is(err, selfupdate.ErrBusy) {
		tl.t.Errorf("a check run while the user was told = %v, want ErrBusy", err)
	}
	if len(tl.answers) == 0 {
		return nil
	}
	answer := tl.answers[0]
	tl.answers = tl.answers[1:]
	return answer
}

func TestCheck_AManagedReleaseIsRecordedAsToldOnlyOnceTheUserHearsOfIt(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	in := m.at("0.1.0")
	in.ExecPath = filepath.Join(t.TempDir(), "Cellar", "trajector", "0.1.0", "bin", "trajector")
	tl := &teller{t: t, in: in, answers: []error{errors.New("no room for the line")}}
	in.Tell = tl.tell
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))

	for day, want := range []bool{true, true, false} {
		c, err := selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt.Add(time.Duration(day) * 24 * time.Hour)}, noFinish(t))
		if err != nil || c.TellManaged != want {
			t.Errorf("check on day %d = %+v, %v; want TellManaged %v", day, c, err, want)
		}
	}
	if len(tl.heard) != 2 {
		t.Errorf("the user was told %d times, want twice: once not heard, once heard", len(tl.heard))
	}
	if s := loadState(t, in); s.ToldManaged != "0.2.0" {
		t.Errorf("state is %+v, want 0.2.0 recorded as told", s)
	}
}

func TestCheck_AFinishGivenUpStaysNamedUntilTheUserHearsOfIt(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	in := m.at("0.1.0")
	tl := &teller{t: t, in: in, answers: []error{errors.New("no room for the line")}}
	in.Tell = tl.tell
	writeState(t, in, `{"installed":"0.1.0","finish_pending":"0.1.0","finish_failures":1}`)
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
	finish := func(context.Context) error { return errors.New("cannot") }

	c, err := selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt}, finish)
	if err != nil || !c.FinishFailed {
		t.Fatalf("Check = %+v, %v; want the finish given up", c, err)
	}
	if s := loadState(t, in); s.FinishPending != "0.1.0" {
		t.Errorf("state is %+v, want 0.1.0 still named unfinished while the user has not heard", s)
	}
	c, err = selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt.Add(24 * time.Hour)}, finish)
	if err != nil || !c.FinishFailed {
		t.Fatalf("Check = %+v, %v; want the finish given up again", c, err)
	}
	if s := loadState(t, in); s.FinishPending != "" || s.FinishFailures != 0 {
		t.Errorf("state is %+v, want nothing named unfinished once the user heard", s)
	}
}

func TestCheck_WithNothingToTellDoesNotTell(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	in := m.at("0.1.0")
	in.Tell = func(c selfupdate.Checked, err error) error {
		t.Errorf("the user was told of %+v, %v", c, err)
		return nil
	}
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))

	if c, err := selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt}, noFinish(t)); err != nil || c.Result != selfupdate.ResultNewest {
		t.Errorf("Check = %+v, %v; want the newest release found", c, err)
	}
}

func TestCheck_AReleaseThatDoesNotStartIsTold(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	in := m.at("0.1.0")
	tl := &teller{t: t, in: in}
	in.Tell = tl.tell
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("these bytes are not a program"))

	if _, err := selfupdate.Check(t.Context(), in, releases.IndexURL(), selfupdate.Window{Now: checkedAt}, noFinish(t)); err == nil {
		t.Fatal("Check = nil, want 0.2.0 found not to start here")
	}
	if len(tl.heard) != 1 || tl.heard[0].Release != "0.2.0" {
		t.Errorf("the user heard %+v, want 0.2.0 once", tl.heard)
	}
}
