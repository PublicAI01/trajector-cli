package selfupdate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

func TestCheck_AfterAnInterruptedUpgradePutsTheKeptBinaryBackAndAsksNothing(t *testing.T) {
	// The 0.2.0 build was put in place and named unfinished, and the
	// upgrade died before the 0.1.0 binary it kept went; the check runs
	// from the 0.2.0 build.
	m := install(t, "the 0.2.0 build, never committed")
	plant(t, m.kept("0.1.0"), "the 0.1.0 binary", time.Now())
	writeState(t, m.at("0.1.0"), `{"installed": "0.1.0", "finish_pending": "0.2.0", "unusable": {"0.2.0": "it did not start"}}`)
	index, asked := releaseIndex(t)

	c, err := selfupdate.Check(t.Context(), m.at("0.2.0"), index, selfupdate.Window{Now: checkedAt}, noFinish(t))
	putBack, ok := errors.AsType[*selfupdate.PutBackError](err)
	if !ok || putBack.Version != "0.1.0" || c.Result != selfupdate.ResultFailed {
		t.Fatalf("Check = %+v, %v; want 0.1.0 put back", c, err)
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("the binary is %q, want the kept 0.1.0 binary put back", got)
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("the release index was asked %d times by a build no longer installed", n)
	}
	s := loadState(t, m.at("0.1.0"))
	if s.Installed != "0.1.0" || s.FinishPending != "" || s.Unusable["0.2.0"] == "" {
		t.Errorf("state is %+v, want 0.1.0 still installed, nothing to finish, and 0.2.0 still recorded against", s)
	}
	if s.Result != selfupdate.ResultFailed || !s.CheckedAt.Equal(checkedAt) || !strings.Contains(s.Error, "0.1.0 was put back") {
		t.Errorf("state is %+v, want the put back recorded as the check's failure", s)
	}
}

func TestCheck_WhoseTimeCannotBeRecordedAsksNothing(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	in := m.at("0.1.0")
	// A directory with something in it holds the state file's name.
	if err := os.MkdirAll(filepath.Join(in.State, "somebody's"), 0o755); err != nil {
		t.Fatal(err)
	}
	index, asked := releaseIndex(t)

	if _, err := selfupdate.Check(t.Context(), in, index, selfupdate.Window{Now: checkedAt}, noFinish(t)); err == nil {
		t.Error("Check = nil, want the state file named as what failed")
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("the release index was asked %d times by a check that could not record when it ran", n)
	}
}

func TestUpgrade_ByHandDropsABackgroundResultItMadeUntrue(t *testing.T) {
	for name, c := range map[string]struct {
		before   string
		publish  string
		released []byte
		left     selfupdate.Result
	}{
		"a failure, by a run that found nothing newer":  {before: "failed", publish: "0.1.0", released: []byte("the 0.1.0 binary")},
		"a failure, by a run that installed":            {before: "failed", publish: "0.2.0"},
		"an install, by a run that installed another":   {before: "upgraded", publish: "0.2.0"},
		"up to date, by a run that found nothing newer": {before: "newest", publish: "0.1.0", released: []byte("the 0.1.0 binary"), left: selfupdate.ResultNewest},
	} {
		t.Run(name, func(t *testing.T) {
			m := install(t, "the 0.1.0 binary")
			in := m.at("0.1.0")
			writeState(t, in, `{"checked_at": "2026-10-12T09:00:00Z", "result": "`+c.before+`", "error": "the index did not answer"}`)
			released := c.released
			if released == nil {
				released = scriptRelease(t, "exit 0", "exit 0")
			}
			releases := fakereleases.New(t)
			releases.Publish(t, c.publish, released)

			if _, err := selfupdate.Upgrade(t.Context(), in, releases.IndexURL(), 0); err != nil {
				t.Fatalf("Upgrade = %v", err)
			}
			if s := loadState(t, in); s.Result != c.left || !s.CheckedAt.Equal(checkedAt) {
				t.Errorf("state is %+v, want result %q and the check's time kept", s, c.left)
			}
		})
	}
}
